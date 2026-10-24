//go:build !dot && !spot

package display

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/feature/detect"
	"github.com/HuskerMinion/techo5/echod/internal/feature/media"
	"github.com/HuskerMinion/techo5/echod/internal/feature/phone"
	"github.com/HuskerMinion/techo5/echod/internal/feature/setup"
	"github.com/HuskerMinion/techo5/echod/internal/feature/video"
	"github.com/HuskerMinion/techo5/echod/internal/feature/xiaozhi"
	esphome "github.com/ygelfand/go-esphome-device"
)

const browserLauncher = "/data/techo5-apps/bin/browser-session"
const appPoll = 50 * time.Millisecond

// browserVideoLimit bounds one handoff: addresses, and the subtitles carried with it (player.py's
// REQUEST_LIMIT).
const browserVideoLimit = 1 << 20

// browserTrack is the name the browser's sound plays under.
const browserTrack = "Local browser"

// The private Pulse sink sends silence all the time the browser is open, so a pause in it is the
// session stalled under load. Ending the track there closed the pipe, parec died on it, and the
// whole browser session went with it.
func init() { media.WaitOutQuiet(browserTrack) }

type browserVideoRequest struct {
	URL      string `json:"url"`
	AudioURL string `json:"audio_url"`
	// The other sounds (the first is audio_url's) and the subtitles the viewer may choose.
	AudioTracks []browserChoice `json:"audio_tracks,omitempty"`
	Subtitles   []browserChoice `json:"subtitles,omitempty"`
}

// browserChoice is a sound, by its address, or subtitles, carried as WebVTT: the browser fetched them
// with its cookies, which nothing here has.
type browserChoice struct {
	Label string `json:"label"`
	URL   string `json:"url,omitempty"`
	VTT   string `json:"vtt,omitempty"`
}

func videoTracks(tracks []browserChoice) []video.Track {
	out := make([]video.Track, 0, len(tracks))
	for _, t := range tracks {
		out = append(out, video.Track{Label: t.Label, URL: t.URL, Text: t.VTT})
	}
	return out
}

// One newline-terminated JSON frame is allowed per lease. Other launcher
// processes retain fd3 while browsing, so accepting a frame must not wait for EOF.
func readBrowserVideo(r io.Reader) (browserVideoRequest, error) {
	var req browserVideoRequest
	data, err := bufio.NewReader(io.LimitReader(r, browserVideoLimit+1)).ReadBytes('\n')
	if err != nil || len(data) > browserVideoLimit {
		return req, errors.New("invalid browser video request")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return browserVideoRequest{}, errors.New("invalid browser video request")
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return browserVideoRequest{}, errors.New("invalid browser video request")
	}
	addresses := []string{req.URL, req.AudioURL}
	for _, t := range req.AudioTracks {
		if t.VTT != "" {
			return browserVideoRequest{}, errors.New("invalid browser video sound")
		}
		addresses = append(addresses, t.URL)
	}
	for _, t := range req.Subtitles {
		if t.URL != "" || t.VTT == "" {
			return browserVideoRequest{}, errors.New("invalid browser video subtitles")
		}
	}
	for i, raw := range addresses {
		if i == 1 && raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
			return browserVideoRequest{}, errors.New("invalid browser video URL")
		}
	}
	return req, nil
}

func (a *localApps) readVideo(pipe *os.File, wake func()) {
	a.videoPipe = pipe
	a.videoReady = make(chan browserVideoRequest, 1)
	ready := a.videoReady
	go func() {
		defer close(ready)
		defer pipe.Close()
		req, err := readBrowserVideo(pipe)
		if err == nil {
			ready <- req
		}
		wake()
	}()
}

// browserPCM preserves the first stereo sample used to establish readiness.
// The channel handoff completes before the media receiver reads its prefix.
type browserPCM struct {
	*os.File
	first [4]byte
	used  int
}

func (p *browserPCM) Read(dst []byte) (int, error) {
	if p.used < len(p.first) {
		n := copy(dst, p.first[p.used:])
		p.used += n
		return n, nil
	}
	return p.File.Read(dst)
}

// localApps serializes requests, process creation, termination and reaping. Only
// the frame loop calls step: acquiring the lease can never race a native paint.
// The lease includes launcher cleanup, not merely the browser's lifetime.
type localApps struct {
	mu               sync.Mutex
	requested        string
	leased           bool
	cmd              *exec.Cmd
	audio            *browserPCM
	audioReady       chan error
	audioTrack       media.ReceivedTrack
	done             chan error
	reaped           bool
	stopping         time.Time
	killed           bool
	kind             string
	videoPipe        *os.File
	videoReady       chan browserVideoRequest
	pending          *browserVideoRequest
	nativeID         uint64
	nativeDone       <-chan struct{}
	returnKind       string
	intent           *appIntent
	control          appControlResponse
	cancelGeneration uint64
	nextRequest      uint64
	voiceBusy        bool
	launchURL        string
	launchPlay       bool // the browser starting for launchURL plays its first YouTube result
	activeURL        string
	returnURL        string
	generation       uint64
	readyPipe        *os.File
	browserReady     chan browserReadyResult
	dictation        *os.File
}

func trustedLauncher(path string) error {
	for p := path; ; p = filepath.Dir(p) {
		st, err := os.Lstat(p)
		if err != nil {
			return err
		}
		owner, ok := st.Sys().(*syscall.Stat_t)
		rootImage := false
		if p == string(filepath.Separator) {
			// Vendor kernels can omit per-mount read-only state from statfs.
			// Writable /data and every launcher ancestor still require root.
			rootImage = errors.Is(syscall.Access(p, 2), syscall.EROFS)
		}
		if !ok || owner.Uid != 0 && !rootImage || st.Mode().Perm()&0022 != 0 || st.Mode()&os.ModeSymlink != 0 {
			return errors.New("browser launcher path must be root-owned and not writable by other users")
		}
		if p == path {
			if !st.Mode().IsRegular() || st.Mode().Perm()&0111 == 0 {
				return errors.New("browser launcher is not executable")
			}
		} else if !st.IsDir() {
			return errors.New("browser launcher parent is not a directory")
		}
		if p == filepath.Dir(p) {
			return nil
		}
	}
}

func (a *localApps) request(kind string) {
	// Normal buttons supersede a deferred MCP request too.
	if kind != "web" && kind != "youtube" && kind != "stop" {
		return
	}
	a.mu.Lock()
	a.intent = nil
	xiaozhi.SetAppHandoffPending(false)
	if kind == "stop" && a.control.State != "error" {
		a.control.State = "starting"
	}
	a.launchURL, a.launchPlay = "", false
	// A second start is deliberately ignored, never queued behind a running app.
	if kind == "stop" || (!a.leased && a.requested == "" && a.pending == nil && a.nativeID == 0) {
		a.requested = kind
	}
	a.mu.Unlock()
}

func (a *localApps) active() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.leased
}

// startCommand is called with mu held and before returning to the frame loop.
func (a *localApps) startCommand(cmd *exec.Cmd, wake func()) error {
	a.leased = true
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
	if err := cmd.Start(); err != nil {
		a.leased = false
		return err
	}
	a.cmd, a.done, a.reaped = cmd, make(chan error, 1), false
	a.stopping, a.killed = time.Time{}, false
	done := a.done
	go func() { done <- cmd.Wait(); wake() }()
	return nil
}

// nativeReturn consumes ownership only when our video really finished or a
// newer session actually began. A DLNA question alone is not a replacement.
func (a *localApps) nativeReturn(st video.State) string {
	return a.nativeReturnAfter(st, video.Get().StopIDDone, func() {})
}

func (a *localApps) nativeReturnAfter(st video.State, completed func(uint64) <-chan struct{}, wake func()) string {
	if st.LastID > a.nativeID {
		a.nativeID, a.returnKind, a.returnURL = 0, "", ""
		return ""
	}
	if st.Active() || st.AskID != 0 {
		return ""
	}
	// Idle publishes before a stopped or naturally-ended decoder has reaped.
	// Keep the destination and public ID until its actual completion barrier.
	if a.nativeDone == nil {
		a.nativeDone = completed(a.nativeID)
		done := a.nativeDone
		go func() { <-done; wake() }()
	}
	select {
	case <-a.nativeDone:
		a.nativeDone = nil
	default:
		return ""
	}
	a.nativeID = 0
	kind := a.returnKind
	// Back from the video to its results page, which must not play the first result again.
	a.launchURL, a.returnURL, a.launchPlay = a.returnURL, "", false
	a.returnKind = ""
	return kind
}

func (a *localApps) nativeStarted(id uint64, err error) {
	if err != nil {
		// A denied request or synchronous startup failure must return the
		// browser, not retry the same native request in a playback loop.
		a.requested, a.returnKind = a.returnKind, ""
		a.launchURL, a.returnURL, a.launchPlay = a.returnURL, "", false
		slog.Warn("browser native video unavailable")
		return
	}
	a.nativeID = id
}

// step returns ownership and whether native framebuffer shadows need invalidation.
func (a *localApps) step(priority bool, now time.Time, wake func()) (bool, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.prepareControl(priority)
	busy := a.voiceBusy
	request := a.requested
	a.requested = ""
	// Explicit stop and higher-priority UI cancel both sides of the handoff.
	// StopID cannot interrupt a newer replacement started by another controller.
	if priority || busy || request == "stop" {
		a.pending, a.returnKind, a.returnURL = nil, "", ""
		if a.nativeID != 0 && a.nativeDone == nil {
			a.nativeDone = video.Get().StopIDDone(a.nativeID)
			done := a.nativeDone
			go func() { <-done; wake() }()
		}
	}
	if a.returnKind == "" && !a.finishNativeStop() {
		return a.leased, false
	}
	if !a.leased {
		if a.nativeID != 0 {
			request = a.nativeReturnAfter(video.Get().State(), video.Get().StopIDDone, wake)
			if request == "" {
				return false, false
			}
		}
		if a.pending != nil {
			// The previous step released the lease. appFrame invalidated the
			// framebuffer before this step can start the native renderer.
			req := a.pending
			a.pending = nil
			if st := video.Get().State(); st.Active() || st.AskID != 0 {
				a.returnKind = ""
				return false, false
			}
			id, err := video.Get().Play(video.Request{URL: req.URL, AudioURL: req.AudioURL, Title: "YouTube", Origin: video.FromLocalApp,
				AudioTracks: videoTracks(req.AudioTracks), Subtitles: videoTracks(req.Subtitles)})
			a.nativeStarted(id, err)
			return false, false
		}
		if busy && !priority && request != "stop" {
			a.requested = request
			return false, false
		}
		if request == "" || request == "stop" || priority {
			if a.intent == nil && a.control.State != "error" {
				a.control.State, a.control.App = "closed", ""
			}
			return false, false
		}
		if err := trustedLauncher(browserLauncher); err != nil {
			slog.Warn("browser session unavailable", "err", err)
			a.controlError("browser launcher unavailable; install the on-device browser runtime")
			return false, false
		}
		cmd := exec.Command(browserLauncher, request)
		// Never inherit API keys, tokens, preload hooks or daemon credentials.
		cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/root", "LANG=C.UTF-8", "TECHO5_VIDEO_FD=3"}
		cmd.Dir = "/"
		if a.launchURL != "" {
			cmd.Env = append(cmd.Env, "TECHO5_APP_URL="+a.launchURL)
			if a.launchPlay {
				cmd.Env = append(cmd.Env, "TECHO5_APP_PLAY=1")
			}
		}
		cmd.Env = append(cmd.Env, "TECHO5_APP_READY_FD=4", "TECHO5_DICTATION_FD=5")
		audio, output, err := os.Pipe()
		if err != nil {
			slog.Warn("browser audio pipe failed", "err", err)
			a.controlError("browser audio pipe unavailable")
			return false, false
		}
		videoInput, videoOutput, err := os.Pipe()
		if err != nil {
			_ = audio.Close()
			_ = output.Close()
			slog.Warn("browser video pipe failed", "err", err)
			a.controlError("browser video pipe unavailable")
			return false, false
		}
		readyInput, readyOutput, err := os.Pipe()
		if err != nil {
			audio.Close()
			output.Close()
			videoInput.Close()
			videoOutput.Close()
			a.controlError("browser readiness pipe unavailable")
			return false, false
		}
		dictation, dictationPeer, err := newDictationPair()
		if err != nil {
			audio.Close()
			output.Close()
			videoInput.Close()
			videoOutput.Close()
			readyInput.Close()
			readyOutput.Close()
			a.controlError("browser dictation socket unavailable")
			return false, false
		}
		cmd.ExtraFiles = []*os.File{videoOutput, readyOutput, dictationPeer}
		cmd.Stdout = output
		if err := a.startCommand(cmd, wake); err != nil {
			readyInput.Close()
			readyOutput.Close()
			dictation.Close()
			dictationPeer.Close()
			a.controlError("browser launcher could not start; check the installed runtime")
			_ = audio.Close()
			_ = output.Close()
			_ = videoInput.Close()
			_ = videoOutput.Close()
			slog.Warn("browser session start failed", "err", err)
			return false, false
		}
		_ = output.Close()
		_ = videoOutput.Close()
		_ = readyOutput.Close()
		_ = dictationPeer.Close()
		a.dictation = dictation
		go serveDictation(dictation, xiaozhi.Get().Dictate, detect.Get().HoldForScreen)
		a.generation++
		a.readReady(readyInput, wake)
		a.activeURL, a.launchURL, a.launchPlay = a.launchURL, "", false
		if a.control.State != "starting" || a.control.App == "" {
			app := "browser"
			if request == "youtube" {
				app = "youtube"
			}
			if a.activeURL != "" && a.control.App != "" {
				app = a.control.App
			}
			a.control = appControlResponse{OK: true, State: "starting", App: app}
		}
		a.kind = request
		a.readVideo(videoInput, wake)
		a.audio = &browserPCM{File: audio}
		a.audioReady = make(chan error, 1)
		pcm, ready := a.audio, a.audioReady
		go func() {
			// Mount/Xauthority/Pulse startup is not a remote sender going quiet.
			_ = pcm.SetReadDeadline(time.Now().Add(15 * time.Second))
			_, err := io.ReadFull(pcm.File, pcm.first[:])
			_ = pcm.SetReadDeadline(time.Time{})
			ready <- err
			wake()
		}()
		return a.leased, false
	}
	if !a.reaped {
		select {
		case err := <-a.done:
			a.reaped = true
			if err != nil && a.stopping.IsZero() {
				slog.Warn("browser session exited", "err", err)
				a.controlError("browser session exited before close; check the installed runtime")
			}
			if a.control.State == "starting" && a.stopping.IsZero() && request != "stop" && !priority && !busy {
				a.controlError("browser exited before readiness; check the installed runtime")
			}
		default:
		}
	}
	if a.videoReady != nil && a.stopping.IsZero() && !priority && !busy && request != "stop" {
		select {
		case req, ok := <-a.videoReady:
			a.videoReady = nil
			if ok {
				a.pending = &req
				a.returnKind = a.kind
				a.returnURL = a.activeURL
			}
		default:
		}
	}
	pid := a.cmd.Process.Pid
	if a.reaped && errors.Is(syscall.Kill(-pid, 0), syscall.ESRCH) {
		if a.videoReady != nil && a.stopping.IsZero() && !priority && !busy && request != "stop" {
			// Child exit can precede scheduling its framed-request reader.
			return true, false
		}
		if a.audio != nil {
			a.audioTrack.Stop()
			_ = a.audio.Close()
			a.audio = nil
		}
		if a.videoPipe != nil {
			_ = a.videoPipe.Close()
			a.videoPipe = nil
		}
		if a.dictation != nil {
			_ = a.dictation.Close()
			a.dictation = nil
		}
		a.videoReady = nil
		a.clearReady()
		if a.intent == nil && a.pending == nil && a.nativeID == 0 && a.control.State != "error" {
			a.control.State, a.control.App = "closed", ""
		}
		a.cmd, a.done, a.leased, a.audioReady = nil, nil, false, nil
		a.audioTrack = media.ReceivedTrack{}
		return false, true
	}
	if a.browserReady != nil && a.stopping.IsZero() && !priority && !busy && request != "stop" {
		select {
		case result := <-a.browserReady:
			if result.generation == a.generation {
				a.clearReady()
			}
			if result.generation == a.generation {
				if a.controlCanceled() {
					request = "stop"
				} else if result.err != nil {
					a.controlError("browser did not acknowledge readiness; check the installed runtime")
					request = "stop"
				} else if a.intent == nil {
					a.control.OK, a.control.State, a.control.Error = true, "open", ""
					xiaozhi.SetAppHandoffPending(false)
				}
			}
		default:
		}
	}
	if a.audioReady != nil && a.stopping.IsZero() && a.pending == nil && !priority && !busy && request != "stop" && !a.reaped {
		select {
		case err := <-a.audioReady:
			a.audioReady = nil
			if err != nil {
				slog.Warn("browser PCM startup failed", "err", err)
				a.controlError("browser audio startup failed; check the installed runtime")
				request = "stop"
			} else {
				// The private Pulse sink emits PCM, never physical mic audio.
				a.audioTrack = media.Get().PlayReceived(browserTrack, a.audio, 48000, 2)
			}
		default:
		}
	}
	if priority || busy || request == "stop" || a.reaped || a.pending != nil {
		if a.stopping.IsZero() {
			a.stopping = now
			a.clearReady()
			a.audioReady = nil
			if a.audio != nil {
				a.audioTrack.Stop()
				_ = a.audio.Close()
			}
			if a.videoPipe != nil {
				_ = a.videoPipe.Close()
			}
			if a.dictation != nil {
				// Ends a dictation in progress along with the session.
				_ = a.dictation.Close()
			}
			a.videoReady = nil
			if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
				slog.Warn("browser session stop failed", "err", err)
			}
		}
	}
	if !a.stopping.IsZero() && now.Sub(a.stopping) >= 3*time.Second && !a.killed {
		a.killed = true
		if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			slog.Warn("browser session kill failed", "err", err)
		}
	}
	return true, false
}

func (d *Display) appRequest(kind string) { d.apps.request(kind); d.wake() }

func (d *Display) appEntities() []esphome.Entity {
	if trustedLauncher(browserLauncher) != nil {
		return nil
	}
	entities := make([]esphome.Entity, 0, 3)
	for _, app := range []struct{ kind, id, name, icon string }{
		{"web", "screen_web_browser", "Web browser", "mdi:web"},
		{"youtube", "screen_youtube", "YouTube", "mdi:youtube"},
		{"stop", "screen_close_browser", "Close browser", "mdi:close"},
	} {
		kind := app.kind
		entities = append(entities, &esphome.Button{Base: esphome.Base{ObjectID: app.id, Name: app.name, Icon: app.icon}, OnPress: func() { d.appRequest(kind) }})
	}
	return entities
}

func (d *Display) appFrame(now time.Time) bool {
	d.mu.Lock()
	if d.apps.active() {
		d.videoLive = false
	}
	busy := d.view.Phase != "idle"
	priority := !d.on || d.updateShown(now) || d.wifiOpen || d.booting
	d.mu.Unlock()
	busy = busy || xiaozhi.AnswerPlaying()
	priority = priority || d.ringing(now).any() || phone.Get().State().Phase != phone.Idle || pinIsOpen() || setup.Get().Waiting()
	d.apps.mu.Lock()
	d.apps.voiceBusy = busy
	d.apps.mu.Unlock()
	owned, released := d.apps.step(priority, now, d.wake)
	if released {
		d.dev.InvalidateFrames()
		d.deckDrawn = ""
	}
	if owned {
		for {
			select {
			case ch := <-d.shots:
				ch <- nil
			default:
				return true
			}
		}
	}
	return false
}

func (d *Display) closeApps() {
	d.apps.request("stop")
	d.apps.step(true, time.Now(), d.wake)
	for d.apps.shutdownPending() {
		d.apps.step(true, time.Now(), d.wake)
		time.Sleep(appPoll)
	}
}
