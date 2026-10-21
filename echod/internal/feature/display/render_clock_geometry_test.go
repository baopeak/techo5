//go:build !dot && !spot

package display

import (
	"image"
	"image/color"
	"testing"
)

func TestOrbitProgressSweepsClockwiseFromTwelve(t *testing.T) {
	lit, track := color.RGBA{40, 180, 150, 255}, color.RGBA{70, 80, 90, 255}
	quadrants := [...]image.Point{{46, 17}, {46, 46}, {17, 46}, {17, 17}}
	for completed := range 5 {
		img := image.NewRGBA(image.Rect(0, 0, 64, 64))
		r := &renderer{paint: paint{dst: img}}
		r.instrumentRing(32, 32, 18, 22, float64(completed)/4, lit, track, 1)
		for quarter, point := range quadrants {
			want := track
			if quarter < completed {
				want = lit
			}
			if got := img.RGBAAt(point.X, point.Y); got != want {
				t.Errorf("%d quarters: quadrant %d = %v, want %v", completed, quarter, got, want)
			}
		}
		if got := img.RGBAAt(32, 32); got != (color.RGBA{}) {
			t.Errorf("%d quarters: progress ring overwrites its hollow center: %v", completed, got)
		}
	}
}
