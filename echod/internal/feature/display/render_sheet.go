//go:build !dot && !spot

package display

import (
	"image"
	"image/color"
)

// The Show settings use a cached, flat shell. Controls and their tap targets are
// composed together over it, so every category uses the same visual hierarchy.
func (r *renderer) settingsPage(sel category, v cardView, pick *pickerView, pickScroll int) {
	fc := r.faces()
	r.pending = r.pending[:0]
	r.settingsBase(sel, v.title, v.blurb)
	for c := category(0); c < categories; c++ {
		r.addZone(zone{r: r.navigationRect(c), kind: zoneCat, cat: c})
	}
	r.addZone(zone{r: r.doneRect(), kind: zoneDone})

	card := r.nextCard(r.w, r.h)
	list := image.Rect(card.Min.X, card.Min.Y+r.headerH(), card.Max.X, card.Max.Y-r.s(8))
	if len(v.rows) == 0 && v.note != "" {
		y := list.Min.Y + r.s(38)
		for _, line := range r.wrap(fc.value, v.note, card.Dx()-2*r.rowIn()) {
			r.text(fc.value, line, card.Min.X+r.rowIn(), y, dim)
			y += r.s(32)
		}
	}

	maxScroll := r.rowList(card, list, v.rows, v.scroll, surface(1), r.base.Pix)
	x := card.Max.X - r.rowIn()
	for _, a := range v.actions {
		cy := card.Min.Y + r.s(34)
		left := r.pillButton(x, cy, a.label, a.style)
		r.addZone(zone{r: image.Rect(left, cy-r.s(24), x, cy+r.s(24)), kind: zoneAction, id: a.id})
		x = left - r.s(12)
	}

	pickMax := 0
	if pick != nil {
		pickMax = r.picker(*pick, pickScroll)
	}
	r.zmu.Lock()
	r.zones, r.pending = r.pending, r.zones
	r.cardMax, r.pickMax = maxScroll, pickMax
	r.zmu.Unlock()
}

func (r *paint) nextCard(w, h int) image.Rectangle {
	return image.Rect(r.railW(), r.cardIn(), w-r.cardIn(), h-r.cardIn())
}

func (r *paint) navigationRect(c category) image.Rectangle {
	top := r.navTop() + int(c)*(r.navH()+r.navGap())
	return image.Rect(r.s(12), top, r.railW()-r.s(14), top+r.navH())
}

func (r *paint) doneRect() image.Rectangle {
	return image.Rect(r.s(12), r.h-r.s(64), r.railW()-r.s(14), r.h-r.s(16))
}

// baseKey describes the cached shell, including all five theme roles.
type baseKey struct {
	sel            category
	title, blurb   string
	g, a, t, d, rl color.RGBA
	w, h           int
}

func (r *renderer) settingsBase(sel category, title, blurb string) {
	key := baseKey{sel, title, blurb, walnut, amber, cream, dim, ember, r.w, r.h}
	if r.base != nil && r.baseKey == key {
		copy(r.dst.Pix, r.base.Pix)
		return
	}
	fc := r.faces()
	r.settingsShell()
	for c := category(0); c < categories; c++ {
		b := r.navigationRect(c)
		fg, face := cream, fc.nav
		if c == sel {
			r.roundFill(b, r.sf(12), amber, amber)
			fg, face = onAccent(), fc.navBold
		}
		r.icon(c, b.Min.X+r.s(25), b.Min.Y+b.Dy()/2, fg)
		r.text(face, categoryNames[c], b.Min.X+r.s(48), b.Min.Y+b.Dy()/2+r.s(8), fg)
	}
	card := r.nextCard(r.w, r.h)
	r.text(fc.header, title, card.Min.X+r.rowIn(), card.Min.Y+r.s(45), cream)
	r.text(fc.sub, r.fit(fc.sub, blurb, card.Dx()-2*r.rowIn()), card.Min.X+r.rowIn(), card.Min.Y+r.s(74), dim)
	r.rule(card.Min.X+r.rowIn(), card.Max.X-r.rowIn(), card.Min.Y+r.headerH()-1, 0.7)
	if r.base == nil || r.base.Rect != r.dst.Rect {
		r.base = image.NewRGBA(r.dst.Rect)
	}
	copy(r.base.Pix, r.dst.Pix)
	r.baseKey = key
}

func (r *renderer) settingsShell() {
	key := baseKey{g: walnut, a: amber, t: cream, d: dim, rl: ember, w: r.w, h: r.h}
	if r.shell != nil && r.shellKey == key {
		copy(r.dst.Pix, r.shell.Pix)
		return
	}
	fc := r.faces()
	r.vgradient(r.dst.Bounds(), walnut, walnut)
	r.text(fc.button, "Settings", r.s(24), r.s(38), cream)
	done := r.doneRect()
	r.roundFill(done, r.sf(12), surface(3), surface(3))
	cy := done.Min.Y + done.Dy()/2
	x := done.Min.X + r.s(22)
	r.aaLine(float64(x+r.s(4)), float64(cy-r.s(6)), float64(x-r.s(2)), float64(cy), r.sf(2), cream)
	r.aaLine(float64(x-r.s(2)), float64(cy), float64(x+r.s(4)), float64(cy+r.s(6)), r.sf(2), cream)
	r.text(fc.button, "Done", done.Min.X+(done.Dx()-r.width(fc.button, "Done"))/2, cy+r.s(8), cream)
	card := r.nextCard(r.w, r.h)
	// The cached card and the list fades must use exactly the same flat color.
	r.roundFill(card, r.cardRad(), surface(1), surface(1))
	if r.shell == nil || r.shell.Rect != r.dst.Rect {
		r.shell = image.NewRGBA(r.dst.Rect)
	}
	copy(r.shell.Pix, r.dst.Pix)
	r.shellKey = key
}
