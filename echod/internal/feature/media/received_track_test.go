package media

import (
	"testing"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/hardware/speaker"
)

func TestReceivedTrackStopEndsPausedAndVoiceSuspendedAudio(t *testing.T) {
	for _, suspended := range []bool{false, true} {
		name := "listener-paused"
		if suspended {
			name = "voice-suspended"
		}
		t.Run(name, func(t *testing.T) {
			s := NewStream(speaker.NewDriver(speaker.New()), speaker.New(), func() {}, func(string) {})
			if suspended {
				s.Suspend() // The receiver waits on the voice gate before its first read.
			}
			src := newQuietSrc()
			handle := s.PlayPCM("Local browser", src, 48000, 2)
			t.Cleanup(handle.Stop)
			if !suspended {
				s.Pause()
			}
			handle.Stop()
			if got := s.Receiving(); got != "" {
				t.Fatalf("terminated application still reported as receiving: %q", got)
			}
			select {
			case <-src.closed:
			case <-time.After(2 * time.Second):
				t.Fatal("held PCM receiver was not canceled")
			}
			if suspended {
				s.Resume()
				if got := s.Receiving(); got != "" {
					t.Fatalf("voice completion revived terminated audio: %q", got)
				}
			}
		})
	}
}

func TestReceivedTrackStopCannotEndSameNamedReplacement(t *testing.T) {
	s := NewStream(speaker.NewDriver(speaker.New()), speaker.New(), func() {}, func(string) {})
	first := s.PlayPCM("Local browser", newQuietSrc(), 48000, 2)
	src := newQuietSrc()
	second := s.PlayPCM("Local browser", src, 48000, 2)
	t.Cleanup(second.Stop)
	first.Stop()
	first.SetPaused(true)
	if _, paused := s.Playing(); paused {
		t.Fatal("retired decoder paused its same-named replacement")
	}
	s.Pause()
	first.SetPaused(false)
	if _, paused := s.Playing(); !paused {
		t.Fatal("retired decoder resumed its same-named replacement")
	}
	if got := s.Receiving(); got != "Local browser" {
		t.Fatalf("old owner stopped replacement: %q", got)
	}
	select {
	case <-src.closed:
		t.Fatal("old owner closed replacement source")
	default:
	}
}

func TestReceivedSeekLeaseRejectsNewerMusicAndStop(t *testing.T) {
	for _, stop := range []bool{false, true} {
		s := NewStream(speaker.NewDriver(speaker.New()), speaker.New(), func() {}, func(string) {})
		video := s.PlayPCM("Video", newQuietSrc(), 48000, 2)
		lease := video.ReleaseForReplacement()
		if lease.generation == 0 {
			t.Fatal("retiring received audio did not preserve replacement ownership")
		}
		var winner *track
		if stop {
			s.Stop()
		} else {
			winner, _ = s.start(&track{item: "New music"})
		}
		src := newQuietSrc()
		if late, ok := lease.Play("Video", src, 48000, 2); ok || late.track != nil {
			t.Fatal("late seek audio overrode a newer music/stop request")
		}
		s.mu.Lock()
		current := s.track
		s.mu.Unlock()
		if current != winner {
			t.Fatal("rejected seek changed the current track")
		}
		select {
		case <-src.closed:
		default:
			t.Fatal("rejected seek source was not closed")
		}
		s.Stop()
	}
}

func TestReceivedSeekLeaseSurvivesVoiceHoldAndRapidReload(t *testing.T) {
	s := NewStream(speaker.NewDriver(speaker.New()), speaker.New(), func() {}, func(string) {})
	s.Suspend()
	defer s.Resume()
	first := s.PlayPCM("Video", newQuietSrc(), 48000, 2)
	lease := first.ReleaseForReplacement()
	second, ok := lease.Play("Video", newQuietSrc(), 48000, 2)
	if !ok {
		t.Fatal("seek lost ownership while voice held the speaker")
	}
	first.Stop()
	lease = second.ReleaseForReplacement()
	third, ok := lease.Play("Video", newQuietSrc(), 48000, 2)
	if !ok {
		t.Fatal("rapid seek failed to transfer ownership")
	}
	defer third.Stop()
	second.Stop()
	s.mu.Lock()
	current := s.track
	s.mu.Unlock()
	if current != third.track {
		t.Fatal("retired seek stopped its replacement")
	}
}

func TestReceivedSeekLeaseCannotBeRecapturedAfterNewerTrackFinished(t *testing.T) {
	s := NewStream(speaker.NewDriver(speaker.New()), speaker.New(), func() {}, func(string) {})
	first := s.PlayPCM("Video", newQuietSrc(), 48000, 2)
	newer, _ := s.start(&track{item: "New music"})
	s.finished(newer, false)
	lease := first.ReleaseForReplacement()
	if lease.generation != 0 {
		t.Fatal("stale decoder recaptured ownership after newer audio ended")
	}
	src := newQuietSrc()
	if _, ok := lease.Play("Video", src, 48000, 2); ok {
		t.Fatal("stale ownership lease revived video audio")
	}
}
