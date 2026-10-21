//go:build !dot && !spot

package display

// Full-panel compositions, intentionally absent from the circular Spot's picker.
const (
	stylePoster  = "poster"
	styleOrbit   = "orbit"
	styleMosaic  = "mosaic"
	styleAlmanac = "almanac"
)

var showClockStyles = []struct{ label, value string }{
	{"Poster", stylePoster},
	{"Orbit", styleOrbit},
	{"Mosaic", styleMosaic},
	{"Almanac", styleAlmanac},
}

func init() {
	clockStyles = append(clockStyles, showClockStyles...)
}
