//go:build !dot && !spot

package display

import (
	"testing"

	"github.com/HuskerMinion/techo5/echod/internal/config"
	"github.com/HuskerMinion/techo5/echod/internal/hardware/touch"
)

func TestClockSwipesNavigateWithTapAssistDisabled(t *testing.T) {
	d := noticeDisplay(t, show8Wide, show8High)
	d.clockUp = true
	if err := config.Set().Screen().ClockTap("nothing"); err != nil {
		t.Fatal(err)
	}
	if err := config.Set().Screen().NoStyleSwipe(false); err != nil {
		t.Fatal(err)
	}
	d.gesture(touch.Gesture{Kind: touch.SwipeLeft, X: show8Wide / 2, Y: show8High / 2})
	if got := config.Get().Screen.ClockStyle; got != styleBig {
		t.Fatalf("left swipe with tap disabled selected %q, want Big", got)
	}
	d.gesture(touch.Gesture{Kind: touch.SwipeRight, X: show8Wide / 2, Y: show8High / 2})
	if got := config.Get().Screen.ClockStyle; got != styleClassic {
		t.Fatalf("right swipe did not return to Classic: %q", got)
	}
	if err := config.Set().Screen().NoStyleSwipe(true); err != nil {
		t.Fatal(err)
	}
	d.gesture(touch.Gesture{Kind: touch.SwipeLeft, X: show8Wide / 2, Y: show8High / 2})
	if got := config.Get().Screen.ClockStyle; got != styleClassic {
		t.Fatalf("disabled swipe changed the clock to %q", got)
	}
	if got := config.Get().Screen.ClockTap; got != "nothing" {
		t.Fatalf("clock navigation changed the tap action to %q", got)
	}
}
