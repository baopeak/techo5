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
	if got := s.Receiving(); got != "Local browser" {
		t.Fatalf("old owner stopped replacement: %q", got)
	}
	select {
	case <-src.closed:
		t.Fatal("old owner closed replacement source")
	default:
	}
}
