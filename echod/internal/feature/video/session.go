//go:build !dot

package video

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/feature/media"
	"github.com/HuskerMinion/techo5/echod/internal/lib/safe"
)

// TrackName is what the video's sound is called as a received track: what the media player and the
// screen's music pages see playing.
const TrackName = "Video"

// queueBytes bounds the frames decoded ahead of the picture, about 22 MB: half a second of the Show
// 5's frames. The sound runs ahead of the picture by about what this holds, since the decoder makes
// both as it goes.
const queueBytes = 22 << 20

// stuckFor is how long the frames may wait full, with the sound not moving and nothing paused, before
// the oldest is dropped: a stream whose sound comes later in the file than its picture would
// otherwise wait forever on a decoder waiting for the frames to be taken.
const stuckFor = 400 * time.Millisecond

// statsEvery is how often a playing video's numbers go in the log.
const statsEvery = 10 * time.Second

// Frame is one picture, ready for the panel (hardware/screen.PresentFrame), and its place in the video.
type Frame struct {
	Pix []byte
	N   int

	owner *session
}

// session is one video, from the request to its end.
type session struct {
	id         uint64
	req        Request
	offset     time.Duration       // absolute VOD position at this decoder's relative time zero
	audioLease media.ReceivedLease // retained through reloads; never recaptured after a seek delay
	u          *url.URL
	ctx        context.Context
	cancel     context.CancelFunc
	screen     Screen
	panelW     int
	panelH     int
	ended      func(s *session, err error, itself bool)
	changed    func()

	// pauses is held through setPaused and through the sound's handover (soundSource.begin), so a pause
	// or a resume never lands between the track becoming the clock and a pause held until then reaching
	// it. Taken before mu.
	pauses sync.Mutex

	mu      sync.Mutex
	cond    *sync.Cond
	info    Info
	rate    Rate
	dec     *decoder
	ready   []*Frame // decoded, oldest first
	free    []*Frame
	made    int  // frames there are buffers for
	most    int  // frames there may be
	read    int  // frames read off the pipe
	videoIn bool // the picture's pipe has ended
	shown   int
	dropped int
	playing bool // started (the first frame has arrived)

	// The clock. With sound, it is how far the room has heard (media.Player.Heard) until the sound runs
	// out, and the wall clock from there; without, the wall clock alone. wallBase is where the wall
	// clock was when it last started or stopped, wallFrom when it started (zero while it is stopped).
	sound          bool
	src            *soundSource
	soundIn        bool  // the sound's pipe has ended
	soundErr       error // explicitly separate sound ended without supplying any PCM
	audioValidated bool  // actual PCM decoded from this request's separate audio, retained by seeks only
	silentTail     bool  // a previously validated seek reached audio EOF before any output PCM
	soundGone      bool  // the speaker was taken from it: the video is over
	wallBase       time.Duration
	wallFrom       time.Time
	wallPaused     bool // a video with no sound, or with its sound run out, paused
	lastHeard      time.Duration
	lastClock      time.Duration // the wall clock's last answer: it does not go back
	lastMove       time.Time     // when the clock last moved, for stuckFor
}

func newSession(id uint64, req Request, u *url.URL, scr Screen, panelW, panelH int) *session {
	ctx, cancel := context.WithCancel(context.Background())
	s := &session{id: id, req: req, u: u, ctx: ctx, cancel: cancel, screen: scr, panelW: panelW, panelH: panelH}
	s.cond = sync.NewCond(&s.mu)
	s.most = max(queueBytes/max(FrameBytes(panelW, panelH), 1), 3)
	return s
}

// scrub takes addresses out of what ffmpeg said, as Redacted does: any of them (the one asked for, a
// redirect's, a playlist's parts) can carry a password or a server's token.
func (s *session) scrub(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(scrubURLs(err.Error()))
}

var reURL = regexp.MustCompile(`(?i)\bhttps?://[^\s'"]+`)

func scrubURLs(msg string) string {
	return reURL.ReplaceAllStringFunc(msg, func(raw string) string {
		if u, err := url.Parse(raw); err == nil {
			return Redacted(u)
		}
		return "the address"
	})
}

// run plays the video until it ends, fails or is stopped.
func (s *session) run(insecure bool) {
	err, itself := s.play(insecure)
	s.cancel()
	s.mu.Lock()
	dec := s.dec
	if s.soundErr != nil {
		err, itself = s.soundErr, false
	}
	s.videoIn = true
	// The frames go with the session: some 22 MB, which nothing reads again. A frame the screen still
	// has comes back to a list nobody takes from, and goes with it.
	s.ready, s.free = nil, nil
	s.cond.Broadcast()
	s.mu.Unlock()
	if dec != nil {
		dec.stop()
	}
	if src := s.sound0(); src != nil {
		soundTurns.Lock()
		src.end()
		soundTurns.Unlock()
	}
	s.ended(s, s.scrub(err), itself)
}

// sound0 is the sound's source, once there is one.
func (s *session) sound0() *soundSource {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.src
}

// stop ends the session from outside: stopped, or replaced by another video. quiet silences what of
// its sound the speaker still has queued, which a video that ends by itself plays out instead.
func (s *session) stop(quiet bool) {
	s.cancel()
	src := s.sound0()
	if src == nil {
		return
	}
	soundTurns.Lock()
	defer soundTurns.Unlock()
	if started := src.end(); quiet && started {
		src.track.Stop()
	}
}

// stopForSeek atomically retires our sound and carries its request ownership into the reload.
func (s *session) stopForSeek() media.ReceivedLease {
	s.cancel()
	soundTurns.Lock()
	defer soundTurns.Unlock()
	if src := s.sound0(); src != nil {
		if src.end() {
			return src.track.ReleaseForReplacement()
		}
	}
	return s.audioLease
}

func (s *session) play(insecure bool) (error, bool) {
	began := time.Now()
	// The network it may reach is everything but the device itself, by the kernel's fence or through
	// the daemon's proxy (fence.go, guard.go): nothing is decoded until one of them holds.
	proxy, err := netGuard(s.ctx)
	if err != nil {
		return err, false
	}
	s.mu.Lock()
	info, cached := s.info, s.rate.Num > 0
	s.mu.Unlock()
	if !cached {
		info, err = probe(s.ctx, s.u.String(), insecure, proxy)
	}
	if s.ctx.Err() != nil {
		return nil, false
	}
	if err != nil {
		return err, false
	}
	if !cached && s.req.AudioURL != "" {
		audio, err := probeMedia(s.ctx, s.req.AudioURL, insecure, proxy, true)
		if s.ctx.Err() != nil {
			return nil, false
		}
		if err != nil {
			return err, false
		}
		info.AudioIndex, info.AudioCodec = audio.AudioIndex, audio.AudioCodec
	}
	s.mu.Lock()
	s.info, s.rate = info, FrameRate(info.FPS)
	s.sound = info.AudioIndex >= 0
	s.mu.Unlock()
	fit := FitIn(info, s.screen)
	slog.Info("video: playing", "id", s.id, "url", Redacted(s.u), "video", info.VideoCodec,
		"size", itoa(info.Width)+"x"+itoa(info.Height), "fps", info.FPS, "shown", itoa(fit.W)+"x"+itoa(fit.H),
		"sound", info.AudioCodec, "silent", !s.sound, "duration", info.Duration.Round(time.Second),
		"probed in", time.Since(began).Round(time.Millisecond))
	s.changed()

	dec, err := start(s.ctx, DecodeArgs(Decoding{URL: s.u.String(), AudioURL: s.req.AudioURL, Info: info, Screen: s.screen, Offset: s.offset, CAFile: caFileIf(), Insecure: insecure, Proxy: proxy}), true, s.sound)
	if err != nil {
		return err, false
	}
	s.mu.Lock()
	s.dec = dec
	s.mu.Unlock()

	if s.sound {
		src := &soundSource{f: dec.audio, s: s, quit: make(chan struct{})}
		if s.req.AudioURL != "" {
			src.firstDone = make(chan struct{})
		}
		s.mu.Lock()
		s.src = src
		s.mu.Unlock()
		safe.Go("video sound", src.begin)
	}
	readErr := s.readFrames(dec.video)
	if s.ctx.Err() != nil {
		return nil, false
	}
	// A damaged frame is already a definitive failure; do not wait on a paused
	// audio handover or hold the picture while waiting for the listener to resume.
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return readErr, false
	}
	// Drain the picture while the first PCM read runs: waiting before readFrames could deadlock
	// the decoder on its full picture pipe. Do not declare success before that read finishes.
	if s.req.AudioURL != "" {
		select {
		case <-s.src.firstDone:
		case <-s.ctx.Done():
		}
	}
	if s.ctx.Err() != nil {
		return nil, false
	}
	s.mu.Lock()
	silentTail, read := s.silentTail, s.read
	s.mu.Unlock()
	if silentTail || read == 0 {
		// Zero-picture playback is never a paused final frame. Diagnose its decoder
		// exit here as well; cancellation must still break a process/audio wait.
		select {
		case <-dec.done:
		case <-s.ctx.Done():
			return nil, false
		}
		if read == 0 || dec.err != nil {
			return dec.why(), false
		}
	}
	drain := time.NewTicker(50 * time.Millisecond)
	defer drain.Stop()
	// The picture has ended: what is left of it is shown, then the video is over.
	for {
		s.mu.Lock()
		left, gone := len(s.ready), s.soundGone
		s.mu.Unlock()
		if gone {
			return nil, false
		}
		// A decoder can exit while buffered picture is paused. Inspect err only
		// after done closes, and do not wait here: active audio may still need the
		// listener to resume before FFmpeg can finish writing its output.
		select {
		case <-dec.done:
			if dec.err != nil {
				return dec.why(), false
			}
		default:
		}
		// Presenting the final frame is not permission to finish a paused seek.
		// Keep the browser lease until the listener resumes or explicitly stops.
		if left == 0 && !s.paused() {
			break
		}
		select {
		case <-s.ctx.Done():
			return nil, false
		case <-drain.C:
		}
	}
	select {
	case <-dec.done:
	case <-s.ctx.Done():
		return nil, false
	}
	s.mu.Lock()
	read = s.read
	s.mu.Unlock()
	switch {
	case s.ctx.Err() != nil:
		return nil, false
	case readErr != nil && !errors.Is(readErr, io.EOF):
		return readErr, false
	case read == 0 || dec.err != nil:
		// No picture at all, or the decoder failed partway (a connection lost, a stream it could not
		// read): not a video that played to its end.
		return dec.why(), false
	}
	return nil, true
}

// readFrames reads the picture off its pipe until it ends.
func (s *session) readFrames(r *os.File) error {
	size := FrameBytes(s.panelW, s.panelH)
	stats := time.NewTicker(statsEvery)
	defer stats.Stop()
	shown, dropped := 0, 0
	for {
		fr := s.buffer()
		if fr == nil {
			return nil // stopped
		}
		if _, err := io.ReadFull(r, fr.Pix[:size]); err != nil {
			s.mu.Lock()
			s.free = append(s.free, fr)
			s.videoIn = true
			s.mu.Unlock()
			return err
		}
		s.mu.Lock()
		fr.N = s.read
		s.read++
		s.ready = append(s.ready, fr)
		first := !s.playing
		s.playing = true
		if first {
			s.lastMove = time.Now()
			if !s.sound {
				s.wallFrom = time.Now()
			}
		}
		s.mu.Unlock()
		if first {
			s.changed()
		}
		select {
		case <-stats.C:
			s.mu.Lock()
			sh, dr := s.shown-shown, s.dropped-dropped
			shown, dropped = s.shown, s.dropped
			s.mu.Unlock()
			slog.Info("video: playing", "id", s.id, "shown", sh, "dropped", dr, "at", s.clock().Round(100*time.Millisecond),
				"fps", float64(sh)/statsEvery.Seconds())
		default:
		}
	}
}

// buffer is a free frame buffer, waiting for one while the frames decoded ahead are as many as there
// may be. Nil once the video is over.
func (s *session) buffer() *Frame {
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		if s.ctx.Err() != nil {
			return nil
		}
		if n := len(s.free); n > 0 {
			fr := s.free[n-1]
			s.free = s.free[:n-1]
			return fr
		}
		if s.made < s.most {
			s.made++
			return &Frame{Pix: make([]byte, FrameBytes(s.panelW, s.panelH))}
		}
		// Full. Waiting is right while the clock moves or the video is paused; full with the clock
		// standing still, the decoder is waiting on these frames to make the sound that would move
		// it, and the oldest goes.
		if len(s.ready) > 0 && !s.pausedLocked() && time.Since(s.lastMove) > stuckFor {
			s.dropped++
			s.lastMove = time.Now()
			fr := s.ready[0]
			s.ready = s.ready[1:]
			return fr
		}
		s.waitLocked(50 * time.Millisecond)
	}
}

// waitLocked waits on the session's condition for at most d. Wants mu.
func (s *session) waitLocked(d time.Duration) {
	t := time.AfterFunc(d, func() {
		s.mu.Lock()
		s.cond.Broadcast()
		s.mu.Unlock()
	})
	s.cond.Wait()
	t.Stop()
}

// at is when frame n is due, from the start of the video.
func (s *session) at(n int) time.Duration {
	r := s.rate
	if r.Num <= 0 {
		r = Rate{30, 1}
	}
	return time.Duration(int64(n) * int64(r.Den) * int64(time.Second) / int64(r.Num))
}

// next is the frame due now, if one is, dropping those a later frame has already overtaken; else how
// long until the next one is due.
func (s *session) next() (*Frame, time.Duration) {
	now := s.clock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.ready) == 0 {
		return nil, 10 * time.Millisecond
	}
	for len(s.ready) > 1 && s.at(s.ready[1].N) <= now {
		s.free = append(s.free, s.ready[0])
		s.ready = s.ready[1:]
		s.dropped++
	}
	fr := s.ready[0]
	if due := s.at(fr.N); due > now+2*time.Millisecond {
		return nil, due - now
	}
	s.ready = s.ready[1:]
	s.shown++
	s.cond.Broadcast()
	return fr, 0
}

// done takes a frame back once the screen has finished with it.
func (s *session) done(fr *Frame) {
	if fr == nil {
		return
	}
	s.mu.Lock()
	s.free = append(s.free, fr)
	s.cond.Broadcast()
	s.mu.Unlock()
}

// clock is how far into the video the room is.
func (s *session) clock() time.Duration {
	s.mu.Lock()
	sound, soundIn := s.sound, s.soundIn
	handed := s.src != nil && s.src.handedOver()
	canceled := s.ctx.Err() != nil
	s.mu.Unlock()
	if sound && !soundIn {
		// The previous decoder may still own the named Video track while this one loads.
		// Its Heard counter is not this decoder's relative clock.
		if !handed || canceled {
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.lastHeard
		}
		at, ok := media.Get().Heard(TrackName)
		s.mu.Lock()
		defer s.mu.Unlock()
		// Only forward: what is queued is counted as it is handed over, so a moment's count can come
		// out short of the last one, and a picture is never sent back for it.
		if ok && at > s.lastHeard {
			s.lastHeard, s.lastMove = at, time.Now()
		}
		return s.lastHeard
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	at := s.wallBase
	if !s.wallFrom.IsZero() && !s.wallPaused {
		at += time.Since(s.wallFrom)
		s.lastMove = time.Now()
	}
	at = max(at, s.lastClock)
	s.lastClock = at
	return at
}

// soundOut is the sound's pipe running out: the clock goes on from where the sound was heard to, by
// the wall clock, so a picture longer than its sound plays to its end.
func (s *session) soundOut() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.soundIn {
		return
	}
	s.soundIn = true
	s.wallBase, s.wallFrom = s.lastHeard, time.Now()
	// A pause held by the sound's track is the wall clock's now.
	if s.src != nil && s.src.handedOver() && media.Get().Receiving() == TrackName {
		if _, p := media.Get().Playing(); p {
			s.wallPaused = true
		}
	}
}

// paused is whether the video is paused: its sound, held by the media player (a pause from here, from
// Home Assistant's media player or the screen), or its own wall clock.
func (s *session) paused() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pausedLocked()
}

func (s *session) pausedLocked() bool {
	if s.sound && !s.soundIn && s.src != nil && s.src.handedOver() {
		if media.Get().Receiving() != TrackName {
			return false
		}
		_, p := media.Get().Playing()
		return p
	}
	return s.wallPaused
}

// setPaused pauses the video or carries it on.
func (s *session) setPaused(on bool) {
	// A canceled decoder must not pause/resume a later track also named Video.
	// Match the handover lock order in soundSource.begin.
	soundTurns.Lock()
	defer soundTurns.Unlock()
	if s.ctx.Err() != nil {
		return
	}
	s.pauses.Lock()
	s.mu.Lock()
	soundClock := s.sound && !s.soundIn && s.src != nil && s.src.handedOver()
	if !soundClock {
		switch {
		case on && !s.wallPaused:
			if !s.wallFrom.IsZero() {
				s.wallBase += time.Since(s.wallFrom)
			}
			s.wallPaused = true
		case !on && s.wallPaused:
			s.wallPaused = false
			if !s.wallFrom.IsZero() {
				s.wallFrom = time.Now()
			}
		}
	}
	s.mu.Unlock()
	if soundClock {
		s.src.track.SetPaused(on)
	}
	s.pauses.Unlock()
	s.changed()
}

// numbers are the session's counts, for State: frames shown and dropped, and whether the picture has
// begun arriving.
func (s *session) numbers() (shown, dropped int, started bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.shown, s.dropped, s.playing
}

// soundSource is the video's sound as the media player reads a received track. It starts the track
// once the first of the sound has come (begin), holds the track open after the sound's pipe ends so
// the picture can carry on and what was queued plays out, and tells the session when the track was
// taken from it (Close from the media player: something else is playing).
type soundSource struct {
	f         *os.File
	s         *session
	quit      chan struct{}
	firstDone chan struct{}       // first read/track handover completed
	track     media.ReceivedTrack // identity, never stop a later track merely called Video

	mu      sync.Mutex
	first   []byte
	started bool // the sound has begun arriving: its track is started, or about to be
	handed  bool // the track is the media player's: the sound is the clock, and a pause is the track's
	closed  bool
	ending  bool
	once    sync.Once
	endOnce sync.Once
}

// soundTurns is held while a video's sound track is started or a session's sound is ended, so the two
// never cross: a track started by a session already over would take the speaker from the next.
var soundTurns sync.Mutex

// begin waits for the first of the sound, and plays it as a received track.
func (a *soundSource) begin() {
	if a.firstDone != nil {
		defer close(a.firstDone)
	}
	buf := make([]byte, 16384)
	n, err := a.f.Read(buf)
	if err != nil || n == 0 {
		if a.s.req.AudioURL != "" {
			a.s.mu.Lock()
			// The source must have supplied real PCM before this seek. Probe metadata
			// alone is not validation, nor is a read error other than a clean pipe EOF.
			tail := n == 0 && a.s.offset > 0 && a.s.audioValidated && errors.Is(err, io.EOF)
			if a.s.ctx.Err() == nil {
				if tail {
					a.s.silentTail = true
				} else {
					a.s.soundErr = errors.New("the separate audio input ended before supplying sound")
					a.s.cancel()
					a.s.cond.Broadcast()
				}
			}
			a.s.mu.Unlock()
			if tail {
				a.s.soundOut()
			}
			return
		}
		a.s.soundOut()
		return
	}
	a.s.mu.Lock()
	a.s.audioValidated = true
	a.s.mu.Unlock()
	// Starting the track and ending a session take turns (soundTurns), so a session stopped meanwhile
	// either never starts its track or knows it has one to stop. Not under a.mu: the clock and the
	// state read it, and they are not to wait on the media player starting a track.
	soundTurns.Lock()
	a.mu.Lock()
	if a.ending || a.s.ctx.Err() != nil {
		a.mu.Unlock()
		soundTurns.Unlock()
		return
	}
	a.first, a.started = buf[:n], true
	a.mu.Unlock()
	var accepted bool
	a.track, accepted = a.s.audioLease.Play(TrackName, a, soundRate, soundChannels)
	if !accepted {
		soundTurns.Unlock()
		return
	}
	media.Get().SetReceivedTrack(TrackName, a.s.req.Title, "", "")
	// Paused before the sound came (covered while loading, or a pause from Home Assistant): the pause is
	// the track's from now, since the sound is the clock. Handed over and taken off the wall clock under
	// one hold of the session's lock (then a.mu, as pausedLocked takes them), and with pauses held until
	// the track has it: a resume in between would go to the wall clock, or to the track before the
	// pause, and be lost.
	a.s.pauses.Lock()
	a.s.mu.Lock()
	a.mu.Lock()
	a.handed = true
	a.mu.Unlock()
	held := a.s.wallPaused
	// A short input can drain on the reader before handover finishes. In that case
	// soundOut already transferred the pause to the wall clock; do not clear it.
	if !a.s.soundIn {
		a.s.wallPaused = false
	}
	a.s.mu.Unlock()
	if held {
		a.track.SetPaused(true)
	}
	a.s.pauses.Unlock()
	soundTurns.Unlock()
}

// handedOver is whether the media player has the sound's track. Until then a pause is the wall
// clock's, which begin hands to the track: a pause between the sound arriving and the track
// starting was neither, and was lost.
func (a *soundSource) handedOver() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.handed
}

func (a *soundSource) Read(p []byte) (int, error) {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return 0, os.ErrClosed
	}
	if len(a.first) > 0 {
		n := copy(p, a.first)
		a.first = a.first[n:]
		a.mu.Unlock()
		return n, nil
	}
	a.mu.Unlock()
	n, err := a.f.Read(p)
	if err == nil || n > 0 {
		return n, nil
	}
	// The sound has run out (or the decoder has gone). The track is held, so the picture keeps its
	// clock and the speaker plays out what it has, until the video is over.
	a.s.soundOut()
	select {
	case <-a.quit:
	case <-a.s.ctx.Done():
	}
	return 0, io.EOF
}

// SetReadDeadline is nothing: the decoder gives up on a silent connection itself (-rw_timeout), and a
// pause in a stream is not the video ending.
func (a *soundSource) SetReadDeadline(time.Time) error { return nil }

// Close is the media player letting the track go. When the session did not ask for it, something else
// took the speaker, and the video ends with its sound.
func (a *soundSource) Close() error {
	a.once.Do(func() {
		a.mu.Lock()
		a.closed = true
		ours := a.ending
		a.mu.Unlock()
		if !ours {
			slog.Info("video: the speaker was taken; the video ends", "id", a.s.id)
			a.s.mu.Lock()
			a.s.soundGone = true
			a.s.mu.Unlock()
			a.s.cancel()
		}
		_ = a.f.Close()
	})
	return nil
}

// end is the session over: the track is let go, and its sound with it once it has played out. It
// says whether the track had started.
func (a *soundSource) end() bool {
	a.mu.Lock()
	a.ending = true
	started := a.started
	a.mu.Unlock()
	a.endOnce.Do(func() {
		close(a.quit)
		if !started {
			_ = a.f.Close()
		}
	})
	return started
}

func itoa(n int) string { return strconv.Itoa(n) }
