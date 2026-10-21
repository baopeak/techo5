//go:build !dot && !spot

package display

import (
	"image/color"
	"math"
)

// instrumentDisc fills solid scanlines and only blends the one-pixel edge.
// Circle extents need a square root per row, not a hypot per covered pixel.
func (r *renderer) instrumentDisc(cx, cy, radius float64, ink color.RGBA) {
	if radius <= 0 {
		return
	}
	outer, inner := radius+0.5, max(0, radius-0.5)
	radius2, edgeScale := radius*radius, 1/(2*radius)
	for y := max(r.dst.Rect.Min.Y, int(math.Floor(cy-outer))); y < min(r.dst.Rect.Max.Y, int(math.Ceil(cy+outer))); y++ {
		dy := float64(y) + 0.5 - cy
		if math.Abs(dy) >= outer {
			continue
		}
		extent := math.Sqrt(outer*outer - dy*dy)
		x0, x1 := int(math.Floor(cx-extent)), int(math.Ceil(cx+extent))
		middle := 0.0
		if math.Abs(dy) < inner {
			middle = math.Sqrt(inner*inner - dy*dy)
		}
		left, right := int(math.Ceil(cx-middle-0.5)), int(math.Floor(cx+middle-0.5))+1
		if middle == 0 {
			left, right = int(cx), int(cx)
		}
		r.span(y, left, right, ink)
		for side := range 2 {
			lo, hi := x0, left
			if side == 1 {
				lo, hi = right, x1
			}
			for x := max(lo, r.dst.Rect.Min.X); x < min(hi, r.dst.Rect.Max.X); x++ {
				dx := float64(x) + 0.5 - cx
				a := clamp01(0.5 + (radius2-dx*dx-dy*dy)*edgeScale)
				r.blendAt(x, y, ink, a)
			}
		}
	}
}

// instrumentRing visits just the two annular spans on each row. Squared radial
// coverage gives a soft one-pixel edge without per-pixel sqrt/atan2. A half-plane
// test against the progress endpoint selects the clockwise arc from twelve.
func (r *renderer) instrumentRing(cx, cy, inner, outer, progress float64, lit, track color.RGBA, opacity float64) {
	if outer <= 0 || inner >= outer {
		return
	}
	inner = max(0, inner)
	sx, co := math.Sincos(progress * 2 * math.Pi)
	outside, inside := outer+0.5, max(0, inner-0.5)
	outer2, inner2, outerScale := outer*outer, inner*inner, 1/(2*outer)
	innerScale := 0.0
	if inner > 0 {
		innerScale = 1 / (2 * inner)
	}
	for y := max(r.dst.Rect.Min.Y, int(math.Floor(cy-outside))); y < min(r.dst.Rect.Max.Y, int(math.Ceil(cy+outside))); y++ {
		dy := float64(y) + 0.5 - cy
		if math.Abs(dy) >= outside {
			continue
		}
		extent := math.Sqrt(outside*outside - dy*dy)
		hole := 0.0
		if math.Abs(dy) < inside {
			hole = math.Sqrt(inside*inside - dy*dy)
		}
		spans := [2][2]int{{int(math.Floor(cx - extent)), int(math.Ceil(cx - hole))}, {int(math.Floor(cx + hole)), int(math.Ceil(cx + extent))}}
		count := 2
		if hole < 1 {
			spans[0][1] = spans[1][1]
			count = 1
		}
		for side := range count {
			for x := max(spans[side][0], r.dst.Rect.Min.X); x < min(spans[side][1], r.dst.Rect.Max.X); x++ {
				dx := float64(x) + 0.5 - cx
				d2 := dx*dx + dy*dy
				a := clamp01(0.5 + (outer2-d2)*outerScale)
				if inner > 0 {
					a = min(a, clamp01(0.5+(d2-inner2)*innerScale))
				}
				if a <= 0 {
					continue
				}
				ink := track
				cross := sx*dy + co*dx
				before := dx >= 0 && cross <= 0
				if progress > 0.5 {
					before = dx >= 0 || cross <= 0
				}
				if progress >= 1 || progress > 0 && before {
					ink = lit
				}
				if a*opacity >= 1 {
					r.span(y, x, x+1, ink)
				} else {
					r.blendAt(x, y, ink, a*opacity)
				}
			}
		}
	}
}
