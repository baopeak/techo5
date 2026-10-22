//go:build !dot

package video

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/feature/media"
)

func TestSeekTargetBoundsAndLive(t *testing.T) {
	for _, tc := range []struct{ at, duration, want time.Duration }{
		{-time.Second, time.Minute, 0},
		{12*time.Second + 125*time.Millisecond, time.Minute, 12*time.Second + 125*time.Millisecond},
		{2 * time.Minute, time.Minute, time.Minute - 33333334*time.Nanosecond - 10*time.Millisecond},
	} {
		got, err := seekTarget(tc.at, tc.duration, 30)
		if err != nil || got != tc.want {
			t.Fatalf("seek(%v,%v) = %v,%v; want %v", tc.at, tc.duration, got, err, tc.want)
		}
	}
	if _, err := seekTarget(time.Second, 0, 30); !errors.Is(err, ErrNotSeekable) {
		t.Fatalf("live stream seek = %v", err)
	}
}

func TestSeekRetainsIdentityPauseAndRelativeTarget(t *testing.T) {
	f := fake(t, 100000, true)
	id, err := f.Play(Request{URL: "http://192.168.1.20/a.mp4", AudioURL: "http://192.168.1.20/audio", Origin: FromLocalApp})
	if err != nil {
		t.Fatal(err)
	}
	until(t, f, Playing)
	f.Covered(true)
	until(t, f, Paused)
	old := f.cur
	if err := f.SeekID(id, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if old.ctx.Err() == nil {
		t.Fatal("seek did not cancel old decoder")
	}
	if st := f.State(); st.ID != id || st.LastID != id || st.Pos < 30*time.Second || st.Pos > 30*time.Second+250*time.Millisecond || st.Origin != FromLocalApp {
		t.Fatalf("seek lost public ownership or target: %+v", st)
	}
	if !f.cur.paused() || !f.heldByUs || !f.covered {
		t.Fatal("seek lost covered pause")
	}
	if err := f.SkipID(id, -10*time.Second); err != nil {
		t.Fatal(err)
	}
	st := until(t, f, Paused)
	if st.ID != id || st.Pos < 20*time.Second || st.Pos > 20*time.Second+250*time.Millisecond {
		t.Fatalf("paused relative seek: %+v", st)
	}
	// Canceled decoder callbacks cannot report a public end/failure for the stable ID.
	f.sessionEnded(old, errors.New("old decoder"), true)
	if st := f.State(); st.Ended == id || st.Failed == id || st.ID != id {
		t.Fatalf("stale EOF: %+v", st)
	}
	f.Covered(false)
	until(t, f, Playing)
	f.Pause()
	if err := f.SeekID(id, 40*time.Second); err != nil {
		t.Fatal(err)
	}
	until(t, f, Paused)
	f.StopID(id)
	f.runs.Wait()
	if st := f.State(); st.Active() || st.LastID != id || st.Ended == id {
		t.Fatalf("stop resurrected seek: %+v", st)
	}
	if err := f.SeekID(id, 0); !errors.Is(err, ErrNoVideo) {
		t.Fatalf("seek stopped video: %v", err)
	}
}

func TestOldSeekCannotAffectReplacement(t *testing.T) {
	f := fake(t, 100000, true)
	first, err := f.Play(Request{URL: "http://192.168.1.20/a.mp4"})
	if err != nil {
		t.Fatal(err)
	}
	until(t, f, Playing)
	if err := f.SeekID(first, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	old := f.cur
	second, err := f.Play(Request{URL: "http://192.168.1.21/b.mp4"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.SkipID(first, 10*time.Second); !errors.Is(err, ErrNoVideo) {
		t.Fatalf("stale seek: %v", err)
	}
	f.sessionEnded(old, errors.New("old seek"), true)
	f.StopID(first)
	st := until(t, f, Playing)
	if st.ID != second || st.LastID != second || st.Ended == first || st.Failed == first {
		t.Fatalf("old seek affected replacement: %+v", st)
	}
}

func TestSeekFrameTimebaseIsRelative(t *testing.T) {
	s := newSession(1, Request{}, nil, Screen{}, 8, 16)
	defer s.cancel()
	s.offset, s.rate, s.wallPaused = 90*time.Second, Rate{30000, 1001}, true
	s.ready = []*Frame{{N: 0}, {N: 1}}
	fr, _ := s.next()
	if fr == nil || fr.N != 0 {
		t.Fatal("seek did not show relative frame zero")
	}
	fr, wait := s.next()
	if fr != nil || wait != 1001*time.Second/30000 {
		t.Fatalf("frame clock used absolute offset: %v, %v", fr, wait)
	}
}

func TestRapidSeekThenStopCannotResurrectDecoder(t *testing.T) {
	f := fake(t, 100000, true)
	id, err := f.Play(Request{URL: "http://192.168.1.20/a.mp4", Origin: FromLocalApp})
	if err != nil {
		t.Fatal(err)
	}
	until(t, f, Playing)
	f.Pause()
	var retired []*session
	for i := 1; i <= 12; i++ {
		f.mu.Lock()
		retired = append(retired, f.cur)
		f.mu.Unlock()
		if err := f.Seek(time.Duration(i) * time.Second); err != nil {
			t.Fatal(err)
		}
	}
	f.StopID(id)
	f.runs.Wait()
	for _, s := range retired {
		if s.ctx.Err() == nil {
			t.Fatal("retired decoder was left running")
		}
		f.sessionEnded(s, errors.New("late decoder failure"), true)
	}
	st := f.State()
	if st.Active() || st.LastID != id || st.Ended == id || st.Failed == id {
		t.Fatalf("stopped seek was resurrected or reported as ended: %+v", st)
	}
}

func TestSeekRejectsUnknownDurationWithoutReplacingDecoder(t *testing.T) {
	f := fake(t, 100000, true)
	id, err := f.Play(Request{URL: "http://192.168.1.20/a.mp4"})
	if err != nil {
		t.Fatal(err)
	}
	until(t, f, Playing)
	f.mu.Lock()
	old := f.cur
	old.mu.Lock()
	old.info.Duration = 0
	old.mu.Unlock()
	f.mu.Unlock()
	if err := f.SeekID(id, time.Second); !errors.Is(err, ErrNotSeekable) {
		t.Fatalf("unknown duration seek: %v", err)
	}
	if err := f.SeekBy(time.Second); !errors.Is(err, ErrNotSeekable) {
		t.Fatalf("unknown duration relative seek: %v", err)
	}
	f.mu.Lock()
	current := f.cur
	f.mu.Unlock()
	if current != old || old.ctx.Err() != nil {
		t.Fatal("rejected seek stopped or replaced its decoder")
	}
	if err := f.SeekID(0, 0); !errors.Is(err, ErrNoVideo) {
		t.Fatalf("zero controller identity sought current video: %v", err)
	}
}

func TestSeekByUsesPendingTargetAndRetainsManualPause(t *testing.T) {
	f := fake(t, 100000, true)
	id, err := f.Play(Request{URL: "http://192.168.1.20/a.mp4"})
	if err != nil {
		t.Fatal(err)
	}
	until(t, f, Playing)
	f.Pause()
	if err := f.Seek(40 * time.Second); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := f.SeekBy(-10 * time.Second); err != nil {
			t.Fatal(err)
		}
	}
	st := until(t, f, Paused)
	if st.ID != id || st.LastID != id || st.Pos < 10*time.Second || st.Pos > 10*time.Second+250*time.Millisecond {
		t.Fatalf("pending relative seeks drifted or changed identity: %+v", st)
	}
	f.Covered(true)
	f.Covered(false)
	if st := f.State(); st.Phase != Paused {
		t.Fatalf("uncover resumed a manually paused seek: %+v", st)
	}
}

func TestSeekEOFReportsOnlyCurrentDecoder(t *testing.T) {
	f := fake(t, 100000, true)
	id, err := f.Play(Request{URL: "http://192.168.1.20/a.mp4", Origin: FromLocalApp})
	if err != nil {
		t.Fatal(err)
	}
	until(t, f, Playing)
	f.mu.Lock()
	old := f.cur
	f.mu.Unlock()
	if err := f.SeekID(id, 20*time.Second); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	current := f.cur
	f.mu.Unlock()
	f.sessionEnded(old, nil, true)
	if st := f.State(); st.ID != id || st.Ended == id {
		t.Fatalf("retired EOF ended the native ownership lease: %+v", st)
	}
	// Deliver the current decoder's EOF deterministically, then retire its fake process.
	f.sessionEnded(current, nil, true)
	current.stop(true)
	f.runs.Wait()
	if st := f.State(); st.Active() || st.Ended != id || st.LastID != id {
		t.Fatalf("current EOF did not complete the stable ownership lease: %+v", st)
	}
}

func TestUnhandedSeekClockAndCanceledPauseAreIsolated(t *testing.T) {
	s := newSession(1, Request{}, nil, Screen{}, 8, 16)
	s.sound = true
	s.src = &soundSource{s: s}
	s.lastHeard = 123 * time.Millisecond
	if got := s.clock(); got != s.lastHeard {
		t.Fatalf("unhanded decoder read another track's clock: %v", got)
	}
	s.cancel()
	s.changed = func() { t.Fatal("canceled decoder emitted pause change") }
	s.setPaused(true)
	if s.wallPaused {
		t.Fatal("canceled decoder accepted a pause")
	}
}

func TestSeekEndGuardPreservesSourceAndOutputFramesWithProbeRounding(t *testing.T) {
	for _, tc := range []struct {
		fps  float64
		tail time.Duration
	}{
		{24, 41666667 * time.Nanosecond},
		{25, 40 * time.Millisecond},
		{24000.0 / 1001, 41708334 * time.Nanosecond},
		{240, 33333334 * time.Nanosecond},
		{50, 40 * time.Millisecond},
	} {
		// Rounded-up duration must leave a full frame after output downsampling too.
		duration := time.Second + 5*time.Millisecond
		target, err := seekTarget(duration+time.Minute, duration, tc.fps)
		if err != nil {
			t.Fatal(err)
		}
		if want := duration - tc.tail - 10*time.Millisecond; target != want {
			t.Fatalf("%v fps end target = %v, want %v", tc.fps, target, want)
		}
		if target > time.Second-tc.tail {
			t.Fatalf("%v fps target can miss the final frame after probe rounding", tc.fps)
		}
	}
}

func TestQueuedTimelineTapsKeepLatestTarget(t *testing.T) {
	f := fake(t, 100000, true)
	id, err := f.Play(Request{URL: "http://192.168.1.20/a.mp4"})
	if err != nil {
		t.Fatal(err)
	}
	until(t, f, Playing)
	f.Pause()
	// Hold execution, not enqueueing: every gesture must remain nonblocking and ordered.
	f.controls.Lock()
	for _, seconds := range []int{10, 80, 20, 60, 30} {
		f.SeekIDAsync(id, time.Duration(seconds)*time.Second)
	}
	f.SkipIDAsync(id, 10*time.Second)
	f.controls.Unlock()
	deadline := time.Now().Add(10 * time.Second)
	for {
		f.mu.Lock()
		done := !f.seeking
		f.mu.Unlock()
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("queued timeline gestures never completed")
		}
		time.Sleep(time.Millisecond)
	}
	st := until(t, f, Paused)
	if st.Pos < 40*time.Second || st.Pos > 40*time.Second+250*time.Millisecond {
		t.Fatalf("rapid gestures were reordered: %+v", st)
	}
}

func TestNewMusicDuringSeekReloadWinsDelayedSoundHandover(t *testing.T) {
	old := newSession(1, Request{}, nil, Screen{}, 8, 16)
	defer old.cancel()
	old.audioLease = media.Get().ReserveReceived()
	reloading := newSession(1, Request{}, nil, Screen{}, 8, 16)
	defer reloading.cancel()
	reloading.audioLease = old.stopForSeek()

	music, musicInput, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer musicInput.Close()
	newMusic := media.Get().PlayReceived("New music", music, 48000, 2)
	defer newMusic.Stop()

	pcm, decoder, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer decoder.Close()
	if _, err := decoder.Write([]byte{0, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	source := &soundSource{f: pcm, s: reloading, quit: make(chan struct{})}
	reloading.src = source
	source.begin()
	if got := media.Get().Receiving(); got != "New music" {
		t.Fatalf("late seek decoder stole newer audio: %q", got)
	}
	if reloading.ctx.Err() == nil || source.handedOver() {
		t.Fatal("overtaken seek decoder retained speaker ownership")
	}
}

func TestValidatedSeparateAudioSilentSeekTailKeepsPausedOwnership(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(map[bool]string{false: "resume", true: "stop"}[stop], func(t *testing.T) {
			f := fake(t, 100000, true)
			decoderEnv = append(decoderEnv, "TECHO5_FAKE_SEEK_TAIL=1")
			id, err := f.Play(Request{URL: "http://192.168.1.20/video", AudioURL: "http://192.168.1.20/audio", Origin: FromLocalApp})
			if err != nil {
				t.Fatal(err)
			}
			until(t, f, Playing)
			f.mu.Lock()
			initial := f.cur
			f.mu.Unlock()
			select {
			case <-initial.sound0().firstDone:
			case <-time.After(5 * time.Second):
				t.Fatal("initial separate audio did not finish its first PCM handover")
			}
			f.Pause()
			if err := f.SeekID(id, 10000*time.Second); err != nil {
				t.Fatal(err)
			}
			until(t, f, Paused) // presents the sole tail frame, leaving no queued picture
			f.mu.Lock()
			tail := f.cur
			f.mu.Unlock()
			if tail == nil {
				t.Fatal("silent seek tail ended before the listener resumed")
			}
			tail.mu.Lock()
			dec := tail.dec
			tail.mu.Unlock()
			select {
			case <-dec.done:
			case <-time.After(5 * time.Second):
				t.Fatal("tail decoder did not exit cleanly")
			}
			// Several drain ticks must not consume the paused playback/browser lease.
			time.Sleep(150 * time.Millisecond)
			if st := f.State(); st.Phase != Paused || st.ID != id || st.LastID != id || st.Failed == id || st.Ended == id {
				t.Fatalf("paused silent tail lost stable playback ownership: %+v", st)
			}
			if stop {
				f.StopID(id)
			} else {
				f.Resume()
			}
			st := until(t, f, Idle)
			if st.Failed == id || (!stop && st.Ended != id) || (stop && st.Ended == id) {
				t.Fatalf("silent tail completion after stop=%v: %+v", stop, st)
			}
			f.runs.Wait()
		})
	}
}

func TestSilentSeekTailStillRequiresCleanDecoderAndPicture(t *testing.T) {
	for _, tc := range []struct {
		name, env string
	}{
		{"decoder failure", "TECHO5_FAKE_SEEK_FAIL=1"},
		{"no picture", "TECHO5_FAKE_SEEK_NO_VIDEO=1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := fake(t, 100000, true)
			decoderEnv = append(decoderEnv, "TECHO5_FAKE_SEEK_TAIL=1", tc.env)
			id, err := f.Play(Request{URL: "http://192.168.1.20/video", AudioURL: "http://192.168.1.20/audio"})
			if err != nil {
				t.Fatal(err)
			}
			until(t, f, Playing)
			f.mu.Lock()
			initial := f.cur
			f.mu.Unlock()
			select {
			case <-initial.sound0().firstDone:
			case <-time.After(5 * time.Second):
				t.Fatal("initial PCM handover did not finish")
			}
			f.Pause()
			if err := f.SeekID(id, 10000*time.Second); err != nil {
				t.Fatal(err)
			}
			st := until(t, f, Idle) // failure must not wait for a paused tail to resume
			if st.Failed != id || st.Ended == id || st.Err == "" {
				t.Fatalf("invalid silent tail was accepted: %+v", st)
			}
			if tc.name == "decoder failure" && !strings.Contains(st.Err, "network failed") {
				t.Fatalf("decoder failure was hidden by the silent-tail fallback: %+v", st)
			}
		})
	}
}

func TestEmptySeparateAudioNeedsPriorPCMSeekAndEOF(t *testing.T) {
	for _, tc := range []struct {
		name      string
		offset    time.Duration
		validated bool
		readError bool
		allow     bool
	}{
		{"initial request", 0, false, false, false},
		{"probe-only seek", time.Second, false, false, false},
		{"zero-offset restart", 0, true, false, false},
		{"validated seek read error", time.Second, true, true, false},
		{"validated seek EOF", time.Second, true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSession(1, Request{AudioURL: "http://192.168.1.20/audio"}, nil, Screen{}, 8, 16)
			defer s.cancel()
			s.offset, s.audioValidated = tc.offset, tc.validated
			s.sound, s.wallPaused = true, true
			pcm, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer pcm.Close()
			writer.Close()
			if tc.readError {
				pcm.Close()
			}
			src := &soundSource{f: pcm, s: s, quit: make(chan struct{})}
			s.src = src
			src.begin()
			if tc.allow {
				if s.ctx.Err() != nil || !s.paused() || !s.soundIn || s.soundErr != nil {
					t.Fatal("clean validated seek EOF did not preserve a paused wall clock")
				}
			} else if s.ctx.Err() == nil || s.soundErr == nil {
				t.Fatal("unvalidated/errored empty separate audio was not rejected")
			}
		})
	}
}

func TestSeparateAudioValidationIsNotInheritedByIndependentPlay(t *testing.T) {
	f := fake(t, 100000, true)
	first, err := f.Play(Request{URL: "http://192.168.1.20/video", AudioURL: "http://192.168.1.20/audio"})
	if err != nil {
		t.Fatal(err)
	}
	until(t, f, Playing)
	f.mu.Lock()
	initial := f.cur
	f.mu.Unlock()
	select {
	case <-initial.sound0().firstDone:
	case <-time.After(5 * time.Second):
		t.Fatal("initial PCM handover did not finish")
	}
	decoderEnv = append(decoderEnv, "TECHO5_FAKE_ZERO_AUDIO_PACKETS=1")
	second, err := f.Play(Request{URL: "http://192.168.1.21/video", AudioURL: "http://192.168.1.21/audio"})
	if err != nil {
		t.Fatal(err)
	}
	st := until(t, f, Idle)
	if second == first || st.Failed != second || st.Ended == second {
		t.Fatalf("new request inherited another video's PCM validation: %+v", st)
	}
}

func TestPausedNonTailDecoderFailuresDoNotWaitForResume(t *testing.T) {
	for _, tc := range []struct {
		name   string
		frames int
		env    string
		want   string
	}{
		{"zero picture while loading", 0, "", ""},
		{"decoder error with queued picture", 2, "TECHO5_FAKE_DECODE_FAIL=1", "network failed"},
		{"partial frame after picture", 1, "TECHO5_FAKE_PARTIAL_VIDEO=1", "unexpected EOF"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := fake(t, tc.frames, false)
			decoderEnv = append(decoderEnv, "TECHO5_FAKE_ZERO_AUDIO_PACKETS=1")
			if tc.env != "" {
				decoderEnv = append(decoderEnv, tc.env)
			}
			req := Request{URL: "http://192.168.1.20/video", Origin: FromLocalApp}
			u, err := CheckURL(req.URL)
			if err != nil {
				t.Fatal(err)
			}
			info, err := ParseProbe(probeMP4)
			if err != nil {
				t.Fatal(err)
			}
			// Install a normal single-input request already paused/covered before
			// decoding starts, so Loading cannot race the pause in this regression.
			s := newSession(1, req, u, f.scr, f.panelW, f.panelH)
			s.info, s.rate, s.wallPaused = info, FrameRate(info.FPS), true
			s.audioLease = media.Get().ReserveReceived()
			s.ended, s.changed = f.sessionEnded, func() { f.Changed.Emit(struct{}{}) }
			f.mu.Lock()
			f.cur, f.lastID, f.covered, f.heldByUs = s, 1, true, true
			f.runs.Add(1)
			f.mu.Unlock()
			go func() {
				defer f.runs.Done()
				s.run(false)
			}()
			st := until(t, f, Idle) // no Resume is sent
			if st.Failed != 1 || st.Ended == 1 || st.Err == "" {
				t.Fatalf("paused decoder failure was hidden: %+v", st)
			}
			if tc.want != "" && !strings.Contains(st.Err, tc.want) {
				t.Fatalf("lost decoder/read failure %q: %+v", tc.want, st)
			}
			f.runs.Wait()
		})
	}
}
