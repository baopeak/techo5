//go:build !dot && !spot

package display

import (
	"image"
	"image/color"
	"math"
	"time"
)

// instrumentTime keeps the numeric clock and its progress rings on the same
// twelve- or twenty-four-hour cycle. Twelve is displayed at both cycle starts,
// but its progress is zero, not a completed ring.
func instrumentTime(now time.Time, h24 bool) (digits [4]int, hours, minutes float64) {
	h := now.Hour()
	cycle := 24
	if !h24 {
		cycle = 12
		h %= cycle
	}
	hours = (float64(h) + float64(now.Minute())/60) / float64(cycle)
	minutes = float64(now.Minute()) / 60
	if !h24 && h == 0 {
		h = 12
	}
	digits = [4]int{h / 10, h % 10, now.Minute() / 10, now.Minute() % 10}
	if !h24 && h < 10 {
		digits[0] = -1
	}
	return
}

// orbitStyle is an instrument, not an analog dial: two thick concentric tracks
// show elapsed hours and minutes, with the reading and localized date alongside.
// Minute steps deliberately avoid another continuously animated weather effect.
func (r *renderer) orbitStyle(s scene, box image.Rectangle) {
	if box.Empty() {
		return
	}
	_, hours, minutes := instrumentTime(s.now, clock24.Load())
	pad := min(r.s(12), box.Dy()/12)
	diameter := min(box.Dy()-2*pad, box.Dx()*48/100)
	if diameter < 8 {
		return
	}
	cx := float64(box.Min.X+pad) + float64(diameter)/2
	cy := float64(box.Min.Y + box.Dy()/2)
	rad := float64(diameter) * 0.46
	stroke := math.Max(2, rad*0.105)
	if s.slideshow != nil {
		// Only the strokes get a neutral backing; the photo remains between rings.
		for _, radius := range [2]float64{rad, rad * 0.70} {
			r.instrumentRing(cx, cy, radius-stroke/4-2, radius+stroke/4+2, 1, surface(1), surface(1), 1)
		}
	}
	r.instrumentArc(cx, cy, rad, stroke, hours, amber)
	r.instrumentArc(cx, cy, rad*0.70, stroke, minutes, cream)
	// Two small start pips make the midnight/noon empty state intentional.
	r.instrumentDisc(cx, cy-rad, stroke*0.28, amber)
	r.instrumentDisc(cx, cy-rad*0.70, stroke*0.28, cream)

	textBox := image.Rect(box.Min.X+diameter+2*pad, box.Min.Y+pad, box.Max.X-pad, box.Max.Y-pad)
	if textBox.Empty() {
		return
	}
	hm, suffix := clockHM(s.now), clockSuffix(s.now)
	// Face sizes are in Show 5 units, whereas the actual box is panel pixels.
	size := min(116, textBox.Dy()*100/max(1, r.s(190)))
	tf := r.styleFace(false, max(16, size))
	if w := r.width(tf, hm); w > textBox.Dx() {
		tf = r.styleFace(false, max(8, size*textBox.Dx()/max(1, w)))
	}
	af := r.styleFace(true, min(28, max(12, size/3)))
	df := r.styleFace(false, min(28, max(12, size/3)))
	gap := min(r.s(20), textBox.Dy()/10)
	block := capHeight(tf) + gap + capHeight(df)
	if suffix != "" {
		block += gap + capHeight(af)
	}
	base := textBox.Min.Y + (textBox.Dy()-block)/2 + capHeight(tf)
	r.text(tf, hm, textBox.Min.X, base, cream)
	if suffix != "" {
		base += gap + capHeight(af)
		r.text(af, suffix, textBox.Min.X, base, dateColor(amber))
	}
	base += gap + capHeight(df)
	r.styleDateIn(df, s, textBox.Min.X, base, -1, textBox.Dx())
}

// instrumentArc draws a minute-stepped progress band without segment overdraw.
func (r *renderer) instrumentArc(cx, cy, radius, width, progress float64, lit color.RGBA) {
	r.instrumentRing(cx, cy, radius-width/4, radius+width/4, progress, lit, lerp(surface(1), dim, 0.28), 1)
	if progress > 0 {
		a := progress*2*math.Pi - math.Pi/2
		r.instrumentDisc(cx+math.Cos(a)*radius, cy+math.Sin(a)*radius, width*0.60, lit)
	}
}

// A digit is six physical little clocks, in row-major order. Directions use
// clock coordinates: north, east, south, west. Parked clocks keep two diagonal
// hands, in a quieter ink, instead of disappearing from the 24-clock matrix.
const (
	mosaicNorth uint8 = iota
	mosaicEast
	mosaicSouth
	mosaicWest
	mosaicPark
)

type mosaicHands [2]uint8

// The hands trace outlines across adjacent circles, not illuminated LED bars.
// Junctions in 3, 4, 6, 8 and 9 necessarily have a one-sided arm: a real clock
// has two hands, not the three that a perfectly closed branching stroke needs.
var mosaicHandTable = [10][6]mosaicHands{
	{{1, 2}, {3, 2}, {0, 2}, {0, 2}, {0, 1}, {0, 3}}, // 0
	{{4, 4}, {2, 2}, {4, 4}, {0, 2}, {4, 4}, {0, 0}}, // 1
	{{1, 1}, {3, 2}, {1, 2}, {0, 3}, {0, 1}, {3, 3}}, // 2
	{{1, 1}, {3, 2}, {1, 1}, {0, 2}, {1, 1}, {0, 3}}, // 3
	{{2, 2}, {2, 2}, {0, 1}, {0, 2}, {4, 4}, {0, 0}}, // 4
	{{1, 2}, {3, 3}, {0, 1}, {3, 2}, {1, 1}, {0, 3}}, // 5
	{{1, 2}, {3, 3}, {0, 2}, {3, 2}, {0, 1}, {0, 3}}, // 6
	{{1, 1}, {3, 2}, {4, 4}, {0, 2}, {4, 4}, {0, 0}}, // 7
	{{1, 2}, {3, 2}, {0, 1}, {3, 2}, {0, 1}, {0, 3}}, // 8
	{{1, 2}, {3, 2}, {0, 1}, {0, 2}, {1, 1}, {0, 3}}, // 9
}

var mosaicDirections = [5][2]float64{{0, -1}, {1, 0}, {0, 1}, {-1, 0}, {-0.70710678, 0.70710678}}

func (r *renderer) mosaicStyle(s scene, box image.Rectangle) {
	if box.Empty() {
		return
	}
	digits, _, _ := instrumentTime(s.now, clock24.Load())
	// Reserve a real footer even in the timer+music box; it shrinks with the
	// available height instead of retaining the ordinary face's fixed date gap.
	foot := min(r.s(64), box.Dy()/3)
	gap := min(r.s(10), max(2, foot/7))
	pitch := math.Min(float64(box.Dy()-foot)/3, float64(box.Dx())/9.2)
	if pitch < 4 {
		return
	}
	width := pitch * 8.75
	left := float64(box.Min.X) + (float64(box.Dx())-width)/2
	top := float64(box.Min.Y) + (float64(box.Dy()-foot)-3*pitch)/2
	for digit, value := range digits {
		x := left + float64(digit)*pitch*2.15
		if digit >= 2 {
			x += pitch * 0.3
		}
		for cell := range 6 {
			hands := mosaicHands{mosaicPark, mosaicPark}
			if value >= 0 {
				hands = mosaicHandTable[value][cell]
			}
			cx := x + (float64(cell%2)+0.5)*pitch
			cy := top + (float64(cell/2)+0.5)*pitch
			r.mosaicDial(cx, cy, pitch, hands)
		}
	}
	// The secondary reading is deliberately much smaller than the matrix.
	fs := min(26, max(10, (foot-2*gap)*100/max(1, r.s(46))))
	tf := r.styleFace(true, fs)
	df := r.styleFace(false, max(10, fs-4))
	base := box.Max.Y - foot + gap + capHeight(tf)
	t := clockText(s.now)
	r.text(tf, t, box.Min.X+(box.Dx()-r.width(tf, t))/2, base, cream)
	r.styleDateIn(df, s, box.Min.X+box.Dx()/2, min(box.Max.Y-gap, base+gap+capHeight(df)), 0, box.Dx())
}

func (r *renderer) mosaicDial(cx, cy, pitch float64, hands mosaicHands) {
	rad := pitch * 0.45
	// Geometric hands do not use text's photo contrast protection. Retain a
	// bounded solid backing under each dial, including over bright photos.
	r.instrumentDisc(cx, cy, pitch*0.49, surface(1))
	r.instrumentRing(cx, cy, rad-math.Max(0.5, pitch*0.009)/2, rad+math.Max(0.5, pitch*0.009)/2, 1, dim, dim, 0.45)
	ink := cream
	if hands[0] == mosaicPark {
		ink = lerp(surface(1), dim, 0.42)
	}
	for i, hand := range hands {
		if i == 1 && hand == hands[0] {
			continue
		}
		d := mosaicDirections[hand]
		w := math.Max(1, pitch*0.032)
		x, y := cx+d[0]*pitch*0.48, cy+d[1]*pitch*0.48
		if hand == mosaicPark {
			r.fxLine(cx, cy, x, y, w, ink, 1)
		} else {
			r.fillRect(image.Rect(int(math.Round(math.Min(cx, x)-w/2)), int(math.Round(math.Min(cy, y)-w/2)),
				int(math.Round(math.Max(cx, x)+w/2)), int(math.Round(math.Max(cy, y)+w/2))), ink)
		}
	}
	r.instrumentDisc(cx, cy, math.Max(1.1, pitch*0.039), amber)
}
