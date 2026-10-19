//go:build !dot

package display

import (
	"log/slog"

	esphome "github.com/ygelfand/go-esphome-device"

	"github.com/HuskerMinion/techo5/echod/internal/config"
)

// Swipe between clock styles has its own switch, independent of the clock's
// tap action. Disabling tap-to-assist must leave clock navigation available.

// styleSwipeOn is whether a swipe across the clock turns its style now.
func styleSwipeOn() bool {
	return !config.Get().Screen.NoStyleSwipe
}

// styleSwipeSwitch is the Home Assistant switch for the setting.
func styleSwipeSwitch() *esphome.Switch {
	s := &esphome.Switch{
		Base: esphome.Base{
			ObjectID: "screen_clock_style_swipe",
			Name:     "Swipe between clock styles",
			Icon:     "mdi:gesture-swipe-horizontal",
			Category: esphome.CategoryConfig,
		},
	}
	s.OnCommand = func(on bool) { setStyleSwipe(s, on) }
	return s
}

// setStyleSwipe saves the setting and tells Home Assistant.
func setStyleSwipe(s *esphome.Switch, on bool) {
	if err := config.Set().Screen().NoStyleSwipe(!on); err != nil {
		slog.Error("saving a setting failed", "setting", s.ObjectID, "err", err)
		s.Set(!config.Get().Screen.NoStyleSwipe)
		return
	}
	s.Set(on)
	slog.Info("setting changed", "setting", s.ObjectID, "using", on)
}
