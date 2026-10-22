//go:build !dot

package video

import (
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"strings"
	"sync"
	"time"

	esphome "github.com/ygelfand/go-esphome-device"

	"github.com/HuskerMinion/techo5/echod/internal/component"
	"github.com/HuskerMinion/techo5/echod/internal/config"
	"github.com/HuskerMinion/techo5/echod/internal/feature/media"
	"github.com/HuskerMinion/techo5/echod/internal/lib/hook"
	"github.com/HuskerMinion/techo5/echod/internal/lib/safe"
)

// Here is whether this device plays videos: the Show does.
const Here = true

func init() {
	component.Register(component.Device, Get(), component.Order(39))
	media.KeepWhileHeld(TrackName)
}

// askFor is how long the screen asks before a DLNA video is taken as not wanted.
const askFor = 30 * time.Second

// pausedFor is how long a video may stay paused before it is stopped.
const pausedFor = 30 * time.Minute

// notNowFor is how long an address the screen said Not now to is refused without asking again, so a
// controller that retries does not keep the question on the screen.
const notNowFor = 10 * time.Minute

// Feature is the video player.
type Feature struct {
	on, dlna     *esphome.Switch
	state, title *esphome.TextSensor
	// lastErr is the Video error sensor: why the last video could not be played, empty once one plays.
	lastErr *esphome.TextSensor

	// Changed fires when there is something new to show or say: a video starting, its first frame,
	// a pause, its end, a question. Listeners must not block.
	Changed hook.Hook[struct{}]

	// controls serializes decoder replacement with stop, pause and cover transitions.
	// mu remains short-held so media callbacks can still read State during a handover.
	controls sync.Mutex

	mu       sync.Mutex
	scr      Screen
	panelW   int
	panelH   int
	seq      uint64
	lastID   uint64
	cur      *session
	ask      *ask
	notNow   map[string]time.Time
	covered  bool
	heldByUs bool // paused because something covered the video, so uncovering carries it on
	ended    uint64
	failed   uint64
	err      string
	errAt    time.Time

	seekQueue []seekCommand
	seekHead  int
	seeking   bool // one worker preserves the order in which gestures were received

	// publish is a nudge to the goroutine that tells Home Assistant.
	publish chan struct{}

	// runs counts decoder sessions and the gesture worker still running, for tests to wait out.
	runs sync.WaitGroup
}

type seekCommand struct {
	id       uint64
	at       time.Duration
	relative bool
}

// ask is a DLNA video waiting on the screen's answer.
type ask struct {
	id    uint64
	req   Request
	until time.Time
}

var (
	once   sync.Once
	shared *Feature
)

// Get is the video player.
func Get() *Feature {
	once.Do(func() { shared = build() })
	return shared
}

func build() *Feature {
	f := &Feature{notNow: map[string]time.Time{}, publish: make(chan struct{}, 1)}
	f.on = &esphome.Switch{
		Base:      esphome.Base{ObjectID: "video", Name: "Video", Icon: "mdi:television-play", Category: esphome.CategoryConfig},
		OnCommand: f.SetOn,
	}
	f.dlna = &esphome.Switch{
		Base:      esphome.Base{ObjectID: "dlna_video", Name: "DLNA video", Icon: "mdi:cast", Category: esphome.CategoryConfig},
		OnCommand: f.SetDLNA,
	}
	f.state = &esphome.TextSensor{Base: esphome.Base{ObjectID: "video_state", Name: "Video state", Icon: "mdi:television-play"}}
	f.lastErr = &esphome.TextSensor{Base: esphome.Base{ObjectID: "video_error", Name: "Video error", Icon: "mdi:television-off"}}
	f.lastErr.Set("")
	f.title = &esphome.TextSensor{Base: esphome.Base{ObjectID: "video_title", Name: "Video title", Icon: "mdi:filmstrip"}}
	f.state.Set(string(Idle))
	f.title.Set("")
	f.Changed.Listen(func(struct{}) {
		select {
		case f.publish <- struct{}{}:
		default:
		}
	})
	safe.Go("video state", f.tell)
	return f
}

func (f *Feature) Name() string { return "video" }

func (f *Feature) Entities() []esphome.Entity {
	return []esphome.Entity{f.on, f.dlna, f.state, f.title, f.lastErr}
}

func (f *Feature) Restore(c config.Config) {
	f.on.Set(c.Video.On)
	f.dlna.Set(c.Video.DLNA)
	slog.Info("restored", "what", "video", "on", c.Video.On, "dlna", c.Video.DLNA, "allowed", len(c.Video.Allowed),
		"decoder", Installed())
}

// tell keeps Home Assistant's two sensors up to date. A video's sound can be paused from outside it
// (Home Assistant's media player, the screen's music controls), which says nothing here: while a video
// is up its state is looked at every second as well, and a change is told to everyone (Changed).
func (f *Feature) tell() {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var last Phase
	var pausedID uint64
	var pausedAt time.Time
	for {
		select {
		case <-f.publish:
		case now := <-tick.C:
			st := f.State()
			// A video left paused this long is over, as a paused track is (media.stoppedFor): the
			// decoder and its frames are not kept waiting forever.
			switch {
			case st.Phase != Paused:
				pausedID = 0
			case st.ID != pausedID:
				pausedID, pausedAt = st.ID, now
			case now.Sub(pausedAt) > pausedFor:
				slog.Info("video: paused too long; stopped", "id", st.ID, "after", pausedFor)
				pausedID = 0
				f.StopID(st.ID)
			}
			if st.Phase == last || (!st.Active() && last == Idle) {
				continue
			}
			f.Changed.Emit(struct{}{})
		}
		st := f.State()
		last = st.Phase
		f.state.Set(string(st.Phase))
		if st.Phase == Playing {
			f.lastErr.Set("")
		}
		title := st.Title
		if title == "" {
			title = st.Host
		}
		if !st.Active() {
			title = ""
		}
		f.title.Set(title)
	}
}

// SetOn is the Video switch, from Home Assistant or the setup page. Off stops what is playing.
func (f *Feature) SetOn(on bool) {
	if err := config.Set().Video().On(on); err != nil {
		slog.Error("saving a setting failed", "setting", "video", "err", err)
		f.on.Set(!on)
		return
	}
	f.on.Set(on)
	slog.Info("setting changed", "setting", "video", "using", on)
	if !on {
		f.Stop()
		closeGuardIdle() // live cameras may still want it
	}
	f.Changed.Emit(struct{}{})
}

// SetDLNA is the DLNA video switch. Off stops a DLNA video that is playing.
func (f *Feature) SetDLNA(on bool) {
	if err := config.Set().Video().DLNA(on); err != nil {
		slog.Error("saving a setting failed", "setting", "dlna_video", "err", err)
		f.dlna.Set(!on)
		return
	}
	f.dlna.Set(on)
	slog.Info("setting changed", "setting", "dlna_video", "using", on)
	if !on {
		if st := f.State(); st.Active() && st.Origin == FromDLNA {
			f.Stop()
		}
	}
	f.Changed.Emit(struct{}{})
}

// UseScreen is the display saying what the picture is for: the canvas, and the panel a frame fills.
func (f *Feature) UseScreen(s Screen, panelW, panelH int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scr, f.panelW, f.panelH = s, panelW, panelH
}

// Play starts a video, in place of whatever video was up. A DLNA video from an address the screen
// has not allowed asks first, and starts if it is allowed.
func (f *Feature) Play(req Request) (uint64, error) {
	c := config.Get().Video
	if !c.On || (req.Origin == FromDLNA && !c.DLNA) {
		return 0, ErrOff
	}
	if !Installed() {
		return 0, ErrNotInstalled
	}
	u, err := CheckURL(req.URL)
	if err != nil {
		return 0, err
	}
	if ownHost(u.Hostname()) {
		return 0, errors.New("the device does not fetch from itself")
	}
	req.URL = u.String()
	if req.AudioURL != "" {
		audio, err := CheckURL(req.AudioURL)
		if err != nil {
			return 0, err
		}
		if ownHost(audio.Hostname()) {
			return 0, errors.New("the device does not fetch from itself")
		}
		req.AudioURL = audio.String()
	}
	f.mu.Lock()
	if f.panelW == 0 {
		f.mu.Unlock()
		return 0, errors.New("the screen is not up")
	}
	f.seq++
	id := f.seq
	if req.Origin == FromDLNA && !c.IsAllowed(req.From, time.Now()) {
		if until, ok := f.notNow[req.From]; ok && time.Now().Before(until) {
			f.mu.Unlock()
			return 0, ErrDeclined
		}
		if f.ask != nil && f.ask.req.From != req.From {
			f.mu.Unlock()
			return 0, ErrBusy
		}
		f.ask = &ask{id: id, req: req, until: time.Now().Add(askFor)}
		f.mu.Unlock()
		slog.Info("video: asking on the screen", "id", id, "from", req.From, "host", u.Hostname())
		time.AfterFunc(askFor, func() { f.Answer(id, false) })
		f.Changed.Emit(struct{}{})
		return id, nil
	}
	f.mu.Unlock()
	if req.Origin == FromDLNA {
		// Its thirty days start again.
		if err := config.Set().Video().Allow(req.From, time.Now()); err != nil {
			slog.Warn("video: remembering the address failed", "err", err)
		}
	}
	f.begin(id, req)
	return id, nil
}

// begin starts session id, ending whatever was playing.
func (f *Feature) begin(id uint64, req Request) {
	f.controls.Lock()
	defer f.controls.Unlock()
	uu, err := CheckURL(req.URL)
	if err != nil {
		return
	}
	f.mu.Lock()
	// Asked again here, under the lock SetOn's stop takes too: an Allow tapped after Video went off, or
	// a start that crossed the switch, plays nothing.
	if c := config.Get().Video; !c.On || (req.Origin == FromDLNA && !c.DLNA) {
		f.mu.Unlock()
		slog.Info("video: not started: videos were turned off", "id", id)
		return
	}
	old := f.cur
	if old != nil {
		old.cancel()
	}
	s := newSession(id, req, uu, f.scr, f.panelW, f.panelH)
	s.ended, s.changed = f.sessionEnded, func() { f.Changed.Emit(struct{}{}) }
	// Whatever covered the last video is told again for this one by the next frame.
	f.cur, f.ask, f.heldByUs, f.covered = s, nil, false, false
	f.lastID = id
	insecure := config.Get().Diag.InsecureTLS
	f.mu.Unlock()
	if old != nil {
		old.stop(true)
	}
	s.audioLease = media.Get().ReserveReceived()
	slog.Info("video: starting", "id", id, "url", Redacted(uu), "origin", req.Origin, "from", req.From)
	f.Changed.Emit(struct{}{})
	f.runs.Add(1)
	safe.Go("video", func() {
		defer f.runs.Done()
		s.run(insecure)
	})
}

// sessionEnded is a session over, by itself, by failing, or stopped.
func (f *Feature) sessionEnded(s *session, err error, itself bool) {
	f.mu.Lock()
	current := f.cur == s
	if current {
		f.cur, f.heldByUs = nil, false
	}
	if current && itself {
		f.ended = s.id
	}
	if current && err != nil {
		f.failed, f.err, f.errAt = s.id, err.Error(), time.Now()
	}
	f.mu.Unlock()
	if current && err != nil {
		// What the screen says too: a video that fails once it has started fails here, not as the
		// action's answer, which was given when it started.
		f.lastErr.Set(clip(err.Error(), 250))
	}
	shown, dropped, _ := s.numbers()
	switch {
	case err != nil:
		slog.Warn("video: could not be played", "id", s.id, "err", err)
	default:
		slog.Info("video: over", "id", s.id, "by itself", itself, "shown", shown, "dropped", dropped)
	}
	if current {
		f.Changed.Emit(struct{}{})
	}
}

// Answer is the screen's answer to the question about video id: Allow remembers the address and plays
// it, Not now leaves it and refuses that address for a minute.
func (f *Feature) Answer(id uint64, allow bool) {
	f.mu.Lock()
	a := f.ask
	if a == nil || a.id != id {
		f.mu.Unlock()
		return
	}
	f.ask = nil
	if !allow {
		f.notNow[a.req.From] = time.Now().Add(notNowFor)
		for addr, until := range f.notNow {
			if time.Now().After(until) {
				delete(f.notNow, addr)
			}
		}
	}
	f.mu.Unlock()
	slog.Info("video: the screen answered", "id", id, "from", a.req.From, "allow", allow)
	if !allow {
		f.Changed.Emit(struct{}{})
		return
	}
	if err := config.Set().Video().Allow(a.req.From, time.Now()); err != nil {
		slog.Warn("video: remembering the address failed", "err", err)
	}
	f.begin(a.id, a.req)
}

// Asking is the question on the screen, if there is one: the video's id, who is asking, and its title.
func (f *Feature) Asking() (id uint64, from, title string, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ask == nil {
		return 0, "", "", false
	}
	host := ""
	if u, err := CheckURL(f.ask.req.URL); err == nil {
		host = u.Hostname()
	}
	title = f.ask.req.Title
	if title == "" {
		title = host
	}
	return f.ask.id, f.ask.req.From, title, true
}

// Stop ends the video, and any question about one.
func (f *Feature) Stop() { f.stop(0) }

func (f *Feature) stop(id uint64) {
	f.controls.Lock()
	defer f.controls.Unlock()
	f.mu.Lock()
	s := f.cur
	if id != 0 && (s == nil || s.id != id) && (f.ask == nil || f.ask.id != id) {
		f.mu.Unlock()
		return
	}
	if s != nil {
		s.cancel()
	}
	f.cur, f.ask, f.heldByUs = nil, nil, false
	f.mu.Unlock()
	if s != nil {
		slog.Info("video: stopped", "id", s.id)
		s.stop(true)
	}
	f.Changed.Emit(struct{}{})
}

// StopID ends video id if it is still the one up: a controller stopping its own video does not stop the
// next one somebody else started.
func (f *Feature) StopID(id uint64) {
	if id != 0 {
		f.stop(id)
	}
}

// Seek moves the current VOD to an absolute position without changing its public identity.
func (f *Feature) Seek(at time.Duration) error { return f.seek(0, at, false) }

// SeekBy moves the current VOD relative to its position, including a pending seek target.
func (f *Feature) SeekBy(by time.Duration) error { return f.seek(0, by, true) }

// SeekID seeks the current VOD to an absolute position, retaining its public identity. Unknown
// durations (including live streams) are not seekable. A stale controller cannot seek a replacement.
func (f *Feature) SeekID(id uint64, at time.Duration) error {
	if id == 0 {
		return ErrNoVideo
	}
	return f.seek(id, at, false)
}

// SkipID moves relative to the current position; repeated taps while loading use the pending target.
func (f *Feature) SkipID(id uint64, by time.Duration) error {
	if id == 0 {
		return ErrNoVideo
	}
	return f.seek(id, by, true)
}

// SeekIDAsync queues an absolute gesture without blocking the input/frame loop.
// Intents are ordered synchronously, not by whichever goroutine gets scheduled first.
func (f *Feature) SeekIDAsync(id uint64, at time.Duration) { f.queueSeek(id, at, false) }

// SkipIDAsync queues a relative gesture in the same order as timeline taps.
func (f *Feature) SkipIDAsync(id uint64, by time.Duration) { f.queueSeek(id, by, true) }

func (f *Feature) queueSeek(id uint64, at time.Duration, relative bool) {
	if id == 0 {
		return
	}
	f.mu.Lock()
	f.seekQueue = append(f.seekQueue, seekCommand{id: id, at: at, relative: relative})
	start := !f.seeking
	if start {
		f.seeking = true
		f.runs.Add(1)
	}
	f.mu.Unlock()
	if start {
		safe.Go("video seek gestures", func() {
			defer f.runs.Done()
			for {
				f.mu.Lock()
				if f.seekHead == len(f.seekQueue) {
					f.seekQueue = f.seekQueue[:0]
					f.seekHead, f.seeking = 0, false
					f.mu.Unlock()
					return
				}
				command := f.seekQueue[f.seekHead]
				f.seekHead++
				f.mu.Unlock()
				_ = f.seek(command.id, command.at, command.relative)
			}
		})
	}
}

func seekTarget(at, duration time.Duration, fps float64) (time.Duration, error) {
	if duration <= 0 {
		return 0, ErrNotSeekable
	}
	// Leave a full source and output frame plus the probe's centisecond
	// rounding margin; downsampling must not round the remaining output to zero.
	rate := FrameRate(fps)
	frame := time.Duration(math.Ceil(float64(time.Second) * float64(rate.Den) / float64(rate.Num)))
	if fps > 0 && fps <= 240 {
		frame = max(frame, time.Duration(math.Ceil(float64(time.Second)/fps)))
	}
	return min(max(at, 0), max(duration-frame-10*time.Millisecond, 0)), nil
}

func (f *Feature) seek(id uint64, at time.Duration, relative bool) error {
	f.controls.Lock()
	defer f.controls.Unlock()
	f.mu.Lock()
	old := f.cur
	if old == nil || (id != 0 && old.id != id) {
		f.mu.Unlock()
		return ErrNoVideo
	}
	old.mu.Lock()
	info, rate, audioValidated := old.info, old.rate, old.audioValidated
	old.mu.Unlock()
	if relative {
		at += old.offset + old.clock()
	}
	target, err := seekTarget(at, info.Duration, info.FPS)
	if err != nil {
		f.mu.Unlock()
		return err
	}
	s := newSession(old.id, old.req, old.u, old.screen, old.panelW, old.panelH)
	s.offset, s.info, s.rate, s.wallPaused = target, info, rate, old.paused()
	s.audioValidated = audioValidated
	s.ended, s.changed = f.sessionEnded, func() { f.Changed.Emit(struct{}{}) }
	// Install before stopping: teardown and late EOF from the old decoder are never a public end.
	f.cur = s
	old.cancel()
	insecure := config.Get().Diag.InsecureTLS
	f.runs.Add(1)
	f.mu.Unlock()
	s.audioLease = old.stopForSeek()
	f.Changed.Emit(struct{}{})
	safe.Go("video seek", func() {
		defer f.runs.Done()
		s.run(insecure)
	})
	return nil
}

// Pause and Resume hold the video where it is and carry it on; Toggle does whichever it is not doing.
func (f *Feature) Pause()  { f.setPaused(true) }
func (f *Feature) Resume() { f.setPaused(false) }

func (f *Feature) Toggle() {
	if st := f.State(); st.Phase == Paused {
		f.Resume()
	} else {
		f.Pause()
	}
}

func (f *Feature) setPaused(on bool) {
	f.controls.Lock()
	defer f.controls.Unlock()
	f.mu.Lock()
	s := f.cur
	f.heldByUs = false
	f.mu.Unlock()
	if s != nil {
		s.setPaused(on)
	}
}

// Covered is the display saying something is over the video (a call, a ring, a turn, a camera): the
// video pauses under it, and carries on when it is gone if it was playing when it came.
func (f *Feature) Covered(on bool) {
	f.controls.Lock()
	defer f.controls.Unlock()
	f.mu.Lock()
	if on == f.covered {
		f.mu.Unlock()
		return
	}
	f.covered = on
	s := f.cur
	held := f.heldByUs
	f.mu.Unlock()
	if s == nil {
		return
	}
	switch {
	case on && !s.paused():
		slog.Info("video: covered; paused under it", "id", s.id)
		s.setPaused(true)
		f.mu.Lock()
		f.heldByUs = true
		f.mu.Unlock()
	case !on && held:
		slog.Info("video: uncovered; going on", "id", s.id)
		f.mu.Lock()
		f.heldByUs = false
		f.mu.Unlock()
		s.setPaused(false)
	}
}

// State is the player now.
func (f *Feature) State() State {
	f.mu.Lock()
	s, a := f.cur, f.ask
	st := State{Phase: Idle, LastID: f.lastID, Ended: f.ended, Failed: f.failed, Err: f.err, ErrAt: f.errAt}
	if a != nil {
		st.AskID = a.id
	}
	f.mu.Unlock()
	switch {
	case s != nil:
		st.ID, st.Title, st.Origin, st.From, st.Host = s.id, s.req.Title, s.req.Origin, s.req.From, Host(s.u)
		shown, dropped, started := s.numbers()
		st.Shown, st.Dropped, st.Frames = shown, dropped, started
		s.mu.Lock()
		st.Dur = s.info.Duration
		s.mu.Unlock()
		st.Pos = s.offset
		st.Phase = Loading
		if started {
			st.Pos += s.clock()
			st.Phase = Playing
			if s.paused() {
				st.Phase = Paused
			}
		}
	case a != nil:
		st.ID, st.Title, st.Origin, st.From, st.Phase = a.id, a.req.Title, a.req.Origin, a.req.From, Asking
		if u, err := CheckURL(a.req.URL); err == nil {
			st.Host = u.Hostname()
		}
	}
	return st
}

// Next is the frame due on the screen now, or how long until one is; ok is false with no picture to
// show. The screen hands each frame back with Done once the next is up.
func (f *Feature) Next() (fr *Frame, wait time.Duration, ok bool) {
	f.mu.Lock()
	s := f.cur
	f.mu.Unlock()
	if s == nil {
		return nil, 0, false
	}
	if _, _, started := s.numbers(); !started {
		return nil, 0, false
	}
	fr, wait = s.next()
	if fr != nil {
		fr.owner = s
	}
	return fr, wait, true
}

// Done takes a frame back.
func (f *Feature) Done(fr *Frame) {
	if fr == nil || fr.owner == nil {
		return
	}
	fr.owner.done(fr)
}

func (f *Feature) Actions() []*esphome.Action {
	return []*esphome.Action{
		{
			// Plays a video full screen: an http or https address (MP4, MKV, MPEG-TS or HLS, H.264 at
			// 720p or less plays best), with an optional title for the screen and the sensor.
			Name: "play_video",
			Args: []esphome.Arg{{Name: "url", Type: esphome.ArgString}, {Name: "title", Type: esphome.ArgString}},
			Run: func(c esphome.Call) (any, error) {
				_, err := f.Play(Request{URL: c.String("url"), Title: c.String("title"), Origin: FromHomeAssistant})
				if err != nil && !strings.HasPrefix(err.Error(), "video: ") {
					err = fmt.Errorf("video: %w", err)
				}
				if err != nil {
					f.lastErr.Set(clip(err.Error(), 250))
				}
				return nil, err
			},
		},
		{Name: "stop_video", Run: func(esphome.Call) (any, error) { f.Stop(); return nil, nil }},
		{Name: "pause_video", Run: func(esphome.Call) (any, error) { f.Pause(); return nil, nil }},
		{Name: "resume_video", Run: func(esphome.Call) (any, error) { f.Resume(); return nil, nil }},
		{
			// Encrypted ESPHome API: absolute seconds, not a delta. VOD only; clamped to its duration.
			Name: "seek_video",
			Args: []esphome.Arg{{Name: "seconds", Type: esphome.ArgInt}},
			Run: func(c esphome.Call) (any, error) {
				return nil, f.Seek(time.Duration(c.Int("seconds")) * time.Second)
			},
		},
	}
}

// ownHost is whether host is one of the device's own addresses, which the kernel would carry over
// loopback: a video is never fetched from the device itself.
func ownHost(host string) bool {
	if strings.Contains(host, "%") {
		return true // a zoned address is one of the device's own links (CheckURL refuses it first)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.Equal(ip) {
			return true
		}
	}
	return false
}

// The package's own calls, which the DLNA renderer and the setup page use on every device.

func Play(req Request) (uint64, error) { return Get().Play(req) }
func Stop()                            { Get().Stop() }
func StopID(id uint64)                 { Get().StopID(id) }
func Seek(at time.Duration) error      { return Get().Seek(at) }
func SeekBy(by time.Duration) error    { return Get().SeekBy(by) }
func Pause()                           { Get().Pause() }
func Resume()                          { Get().Resume() }
func Current() State                   { return Get().State() }
func SetOn(on bool)                    { Get().SetOn(on) }
func SetDLNA(on bool)                  { Get().SetDLNA(on) }
func Listen(fn func())                 { Get().Changed.Listen(func(struct{}) { fn() }) }

// DLNAOn is whether the DLNA renderer takes videos now.
func DLNAOn() bool {
	c := config.Get().Video
	return c.On && c.DLNA && Installed()
}
