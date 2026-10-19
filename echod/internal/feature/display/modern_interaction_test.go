//go:build !dot && !spot

package display

import (
	"image"
	"image/color"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/config"
)

func TestModernThemesKeepSelectedControlTextReadable(t *testing.T) {
	saved := current()
	t.Cleanup(func() { applyTheme(saved) })
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	luminance := func(c color.RGBA) float64 {
		linear := func(v uint8) float64 {
			f := float64(v) / 255
			if f <= 0.04045 {
				return f / 12.92
			}
			return math.Pow((f+0.055)/1.055, 2.4)
		}
		return 0.2126*linear(c.R) + 0.7152*linear(c.G) + 0.0722*linear(c.B)
	}
	for _, name := range []string{"Light", "Dark"} {
		if err := config.Set().Screen().Theme(name); err != nil {
			t.Fatal(err)
		}
		applyTheme(current())
		a, b := luminance(onAccent()), luminance(amber)
		ratio := (math.Max(a, b) + 0.05) / (math.Min(a, b) + 0.05)
		if ratio < 4.5 {
			t.Errorf("%s selected control contrast %.2f is below WCAG AA", name, ratio)
		}
	}
}

func TestBigClockDigitsStillOpenCalendar(t *testing.T) {
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	if err := config.Set().Screen().ClockStyle(styleBig); err != nil {
		t.Fatal(err)
	}
	r := newRenderer(image.NewRGBA(image.Rect(0, 0, show8Wide, show8High)))
	r.draw(scene{now: time.Date(2026, 10, 19, 14, 37, 0, 0, time.UTC), phase: "idle"})
	if !r.dateTapped(image.Pt(show8Wide/2, show8High/2)) {
		t.Fatal("tapping the Big clock's time no longer reaches its calendar target")
	}
}
