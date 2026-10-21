//go:build !dot && !spot

package display

import (
	"fmt"
	"image"
	"image/color"
	"strconv"
	"strings"
	"time"

	"golang.org/x/image/font"

	"github.com/HuskerMinion/techo5/echod/internal/lib/locale"
)

// editorialText fits the selected screen font to a real layout slot. Using the
// full ascent and descent (not only digit cap height) also leaves room for
// localized weekday accents. align is -1 for left, 0 for center, 1 for right.
func (r *renderer) editorialText(text string, slot image.Rectangle, bold bool, size, align int, ink color.Color) {
	if text == "" || slot.Empty() {
		return
	}
	face := r.styleFace(bold, size)
	bounds, _ := font.BoundString(face, text)
	width := max(r.width(face, text), bounds.Max.X.Ceil()-min(0, bounds.Min.X.Floor()))
	height := face.Metrics().Ascent.Ceil() + face.Metrics().Descent.Ceil()
	for width > slot.Dx() || height > slot.Dy() {
		// Integer font metrics can round one pixel above a proportional fit.
		// Move to a genuinely smaller cached face until the glyph fits, rather
		// than dropping a clock digit after a single approximate resize.
		factor := min(float64(slot.Dx())/float64(max(1, width)), float64(slot.Dy())/float64(max(1, height)))
		next := min(size-1, int(float64(size)*factor))
		if next < 1 || r.s(size) <= 8 {
			return
		}
		size = next
		face = r.styleFace(bold, size)
		bounds, _ = font.BoundString(face, text)
		width = max(r.width(face, text), bounds.Max.X.Ceil()-min(0, bounds.Min.X.Floor()))
		height = face.Metrics().Ascent.Ceil() + face.Metrics().Descent.Ceil()
	}
	x := slot.Min.X - min(0, bounds.Min.X.Floor())
	if align == 0 {
		x += (slot.Dx() - width) / 2
	} else if align > 0 {
		x += slot.Dx() - width
	}
	base := slot.Min.Y + (slot.Dy()-height)/2 + face.Metrics().Ascent.Ceil()
	r.text(face, text, x, base, ink)
}

// posterStyle is an asymmetric typographic broadside: two huge stacked figures
// on the left and a much quieter date column on the right. There are no cards,
// split seams or background panels, so slideshow photography stays visible.
func (r *renderer) posterStyle(s scene, box image.Rectangle) {
	if box.Empty() {
		return
	}
	gap := min(r.s(32), box.Dx()/20)
	left := image.Rect(box.Min.X, box.Min.Y, box.Min.X+box.Dx()*53/100, box.Max.Y)
	right := image.Rect(left.Max.X+gap, box.Min.Y, box.Max.X, box.Max.Y)
	hour, minute, _ := strings.Cut(clockHM(s.now), ":")
	mid := left.Min.Y + left.Dy()/2
	r.editorialText(hour, image.Rect(left.Min.X, left.Min.Y, left.Max.X, mid), true, 250, 1, cream)
	// The shorter lower line is inset at the left, rather than boxed in a tile.
	r.editorialText(minute, image.Rect(left.Min.X+left.Dx()/8, mid, left.Max.X, left.Max.Y), true, 250, 1, amber)
	line := max(1, r.s(2))
	ruleY := right.Min.Y + right.Dy()/5
	r.fillRect(image.Rect(right.Min.X, ruleY, right.Min.X+right.Dx()/3, min(right.Max.Y, ruleY+line)), amber)
	r.editorialText(fmt.Sprintf("%04d", s.now.Year()), image.Rect(right.Min.X, right.Min.Y, right.Max.X, ruleY), false, 24, -1, dim)
	r.editorialText(locale.Weekday(s.now, screenLang()), image.Rect(right.Min.X, ruleY+line, right.Max.X, right.Min.Y+right.Dy()/2), true, 58, -1, cream)
	r.editorialText(locale.MonthDay(s.now, screenLang()), image.Rect(right.Min.X, right.Min.Y+right.Dy()/2, right.Max.X, right.Min.Y+right.Dy()*3/4), false, 38, -1, dateColor(dim))
	bottom := image.Rect(right.Min.X, right.Min.Y+right.Dy()*3/4, right.Max.X, right.Max.Y)
	period := clockSuffix(s.now)
	if alarm := strings.TrimSpace(alarmSuffix(s)); alarm != "" {
		// Give the period a separate slot: even a long localized alarm cannot
		// displace the 12-hour clock's AM/PM indicator.
		periodWidth := 0
		if period != "" {
			periodWidth = bottom.Dx() / 4
			r.editorialText(period, image.Rect(bottom.Min.X, bottom.Min.Y, bottom.Min.X+periodWidth, bottom.Max.Y), true, 26, -1, amber)
			periodWidth += min(gap/2, bottom.Dx()/12)
		}
		r.editorialText(alarm, image.Rect(bottom.Min.X+periodWidth, bottom.Min.Y, bottom.Max.X, bottom.Max.Y), false, 20, -1, dim)
	} else {
		r.editorialText(period, bottom, true, 26, -1, amber)
	}
	// Like Big, the figures and date both open the existing current calendar.
	r.setDateAt(box)
}

// almanacMonth is a Monday-first month in the scene's own local time zone.
// Empty cells are zero; no dates from adjacent months masquerade as this month.
// Keeping the model independent of drawing makes century leap years and year
// boundaries testable without font, theme or framebuffer fixtures.
type almanacMonth struct {
	year  int
	month time.Month
	first int
	days  int
	rows  int
	today int
}

func almanacMonthFor(now time.Time) almanacMonth {
	year, month, today := now.Date()
	first := time.Date(year, month, 1, 12, 0, 0, 0, now.Location())
	offset := (int(first.Weekday()) + 6) % 7
	days := time.Date(year, month+1, 0, 12, 0, 0, 0, now.Location()).Day()
	return almanacMonth{year: year, month: month, first: offset, days: days, rows: (offset + days + 6) / 7, today: today}
}

func (m almanacMonth) dayAt(row, column int) int {
	if row < 0 || row >= m.rows || column < 0 || column >= 7 {
		return 0
	}
	day := row*7 + column - m.first + 1
	if day < 1 || day > m.days {
		return 0
	}
	return day
}

// almanacStyle juxtaposes an oversized day-of-month and useful time with a real
// compact month grid. The localized weekday headings explicitly label its
// Monday-first convention; no events or weather are fabricated to fill space.
func (r *renderer) almanacStyle(s scene, box image.Rectangle) {
	if box.Empty() {
		return
	}
	month := almanacMonthFor(s.now)
	gap := min(r.s(34), box.Dx()/24)
	left := image.Rect(box.Min.X, box.Min.Y, box.Min.X+box.Dx()*35/100, box.Max.Y)
	grid := image.Rect(left.Max.X+gap, box.Min.Y, box.Max.X, box.Max.Y)
	r.editorialText(locale.Weekday(s.now, screenLang()), image.Rect(left.Min.X, left.Min.Y, left.Max.X, left.Min.Y+left.Dy()/6), true, 28, -1, dateColor(dim))
	r.editorialText(fmt.Sprintf("%02d", month.today), image.Rect(left.Min.X, left.Min.Y+left.Dy()/6, left.Max.X, left.Min.Y+left.Dy()*2/3), true, 190, -1, cream)
	// Time and suffix are measured as one string, preserving AM/PM with every
	// selected font and keeping the calendar readable when music/timers shrink it.
	r.editorialText(clockText(s.now), image.Rect(left.Min.X, left.Min.Y+left.Dy()*2/3, left.Max.X, left.Min.Y+left.Dy()*5/6), true, 58, -1, amber)
	r.editorialText(strings.TrimSpace(alarmSuffix(s)), image.Rect(left.Min.X, left.Min.Y+left.Dy()*5/6, left.Max.X, left.Max.Y), false, 20, -1, dim)

	headerHeight := grid.Dy() / 7
	r.editorialText(fmt.Sprintf("%02d / %04d", month.month, month.year), image.Rect(grid.Min.X, grid.Min.Y, grid.Max.X, grid.Min.Y+headerHeight), true, 32, -1, cream)
	weekTop := grid.Min.Y + headerHeight
	weekHeight := grid.Dy() / 8
	dayTop := weekTop + weekHeight
	// 2024-01-01 was Monday. AddDate, rather than 24-hour durations, preserves
	// local weekday labels even in zones with daylight-saving transitions.
	monday := time.Date(2024, time.January, 1, 12, 0, 0, 0, s.now.Location())
	for column := range 7 {
		x0 := grid.Min.X + grid.Dx()*column/7
		x1 := grid.Min.X + grid.Dx()*(column+1)/7
		r.editorialText(locale.ShortWeekday(monday.AddDate(0, 0, column), screenLang()), image.Rect(x0, weekTop, x1, dayTop), false, 20, 0, dim)
	}
	r.fillRect(image.Rect(grid.Min.X, dayTop, grid.Max.X, min(grid.Max.Y, dayTop+max(1, r.s(1)))), dim)
	for row := range month.rows {
		y0 := dayTop + (grid.Max.Y-dayTop)*row/month.rows
		y1 := dayTop + (grid.Max.Y-dayTop)*(row+1)/month.rows
		for column := range 7 {
			day := month.dayAt(row, column)
			if day == 0 {
				continue
			}
			x0 := grid.Min.X + grid.Dx()*column/7
			x1 := grid.Min.X + grid.Dx()*(column+1)/7
			cell := image.Rect(x0, y0, x1, y1)
			var ink color.Color = cream
			if day == month.today {
				// A contained disc makes today unambiguous in either theme; text
				// uses the existing accent-contrast role, not fixed white ink.
				radius := max(0, min(cell.Dx(), cell.Dy())/2-2)
				r.fxDisc(float64(x0+x1)/2, float64(y0+y1)/2, float64(radius), amber, 1)
				ink = chosenColor(onAccent())
				cell = cell.Inset(max(1, min(cell.Dx(), cell.Dy())/8))
			}
			r.editorialText(strconv.Itoa(day), cell, day == month.today, 28, 0, ink)
		}
	}
	r.setDateAt(box)
}
