//go:build !dot && !spot

package display

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/feature/media"
	"github.com/HuskerMinion/techo5/echod/internal/feature/phone"
	"github.com/HuskerMinion/techo5/echod/internal/feature/setup"
	esphome "github.com/ygelfand/go-esphome-device"
)

const browserLauncher = "/data/techo5-apps/bin/browser-session"
const appPoll = 50 * time.Millisecond

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
	mu         sync.Mutex
	requested  string
	leased     bool
	cmd        *exec.Cmd
	audio      *browserPCM
	audioReady chan error
	audioTrack media.ReceivedTrack
	done       chan error
	reaped     bool
	stopping   time.Time
	killed     bool
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
	if kind != "web" && kind != "youtube" && kind != "stop" {
		return
	}
	a.mu.Lock()
	// A second start is deliberately ignored, never queued behind a running app.
	if kind == "stop" || (!a.leased && a.requested == "") {
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
	go func() { a.done <- cmd.Wait(); wake() }()
	return nil
}

// step returns ownership and whether native framebuffer shadows need invalidation.
func (a *localApps) step(priority bool, now time.Time, wake func()) (bool, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	request := a.requested
	a.requested = ""
	if !a.leased {
		if request == "" || request == "stop" || priority {
			return false, false
		}
		if err := trustedLauncher(browserLauncher); err != nil {
			slog.Warn("browser session unavailable", "err", err)
			return false, false
		}
		cmd := exec.Command(browserLauncher, request)
		// Never inherit API keys, tokens, preload hooks or daemon credentials.
		cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/root", "LANG=C.UTF-8"}
		cmd.Dir = "/"
		audio, output, err := os.Pipe()
		if err != nil {
			slog.Warn("browser audio pipe failed", "err", err)
			return false, false
		}
		cmd.Stdout = output
		if err := a.startCommand(cmd, wake); err != nil {
			_ = audio.Close()
			_ = output.Close()
			slog.Warn("browser session start failed", "err", err)
			return false, false
		}
		_ = output.Close()
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
			}
		default:
		}
	}
	pid := a.cmd.Process.Pid
	if a.reaped && errors.Is(syscall.Kill(-pid, 0), syscall.ESRCH) {
		if a.audio != nil {
			a.audioTrack.Stop()
			_ = a.audio.Close()
			a.audio = nil
		}
		a.cmd, a.done, a.leased, a.audioReady = nil, nil, false, nil
		a.audioTrack = media.ReceivedTrack{}
		return false, true
	}
	if a.audioReady != nil && a.stopping.IsZero() && !priority && request != "stop" && !a.reaped {
		select {
		case err := <-a.audioReady:
			a.audioReady = nil
			if err != nil {
				slog.Warn("browser PCM startup failed", "err", err)
				request = "stop"
			} else {
				// The private Pulse sink emits PCM, never physical mic audio.
				a.audioTrack = media.Get().PlayReceived("Local browser", a.audio, 48000, 2)
			}
		default:
		}
	}
	if priority || request == "stop" || a.reaped {
		if a.stopping.IsZero() {
			a.stopping = now
			a.audioReady = nil
			if a.audio != nil {
				a.audioTrack.Stop()
				_ = a.audio.Close()
			}
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
	priority := !d.on || d.view.Phase != "idle" || d.updateShown(now) || d.wifiOpen || d.booting
	d.mu.Unlock()
	priority = priority || d.ringing(now).any() || phone.Get().State().Phase != phone.Idle || pinIsOpen() || setup.Get().Waiting()
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
	for d.apps.active() {
		d.apps.step(true, time.Now(), d.wake)
		time.Sleep(appPoll)
	}
}
