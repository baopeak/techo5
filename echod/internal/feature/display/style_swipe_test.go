//go:build !dot

package display

import (
	"path/filepath"
	"testing"

	"github.com/HuskerMinion/techo5/echod/internal/config"
)

// Disabling tap-to-assist must not disable a separate clock navigation gesture.
// The swipe switch remains authoritative for every tap action.
func TestClockTapAndStyleSwipeAreIndependent(t *testing.T) {
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	for _, tap := range []string{"nothing", "", "dashboard", "deck"} {
		t.Run(tap, func(t *testing.T) {
			if err := config.Set().Screen().ClockTap(tap); err != nil {
				t.Fatal(err)
			}
			if err := config.Set().Screen().NoStyleSwipe(false); err != nil {
				t.Fatal(err)
			}
			if !styleSwipeOn() {
				t.Fatal("enabled clock swipes were blocked by the tap action")
			}
			if err := config.Set().Screen().NoStyleSwipe(true); err != nil {
				t.Fatal(err)
			}
			if styleSwipeOn() {
				t.Fatal("disabled clock swipes were enabled by the tap action")
			}
		})
	}
}
