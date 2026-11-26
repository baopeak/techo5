//go:build !dot && !spot

package display

import (
	"image"
	"image/draw"
	"strings"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/feature/video"
)

// What the video page draws on the canvas (video.go): while frames come, only the controls, over a
// clear canvas that the panel blends over the picture; before the first frame, and when a video could
// not be played, a page of its own. In the Show 5's pixels.
const (
	videoStripH  = 180 // timeline above the controls' row
	videoButtonW = 84
	videoButtonH = 76
	videoGap     = 16
	videoIcon    = 44
)

// videoButtons are the controls, left to right: play or pause and stop on the left, quieter and louder
// on the right.
func (r *renderer) videoButtons() (play, stop, quieter, louder image.Rectangle) {
	w, h, gap := r.s(videoButtonW), r.s(videoButtonH), r.s(videoGap)
	y := r.h - r.s(112) + (r.s(112)-h)/2
	x := r.margin
	play = image.Rect(x, y, x+w, y+h)
	stop = play.Add(image.Pt(w+gap, 0))
	louder = image.Rect(r.w-r.margin-w, y, r.w-r.margin, y+h)
	quieter = louder.Sub(image.Pt(w+gap, 0))
	return play, stop, quieter, louder
}

// videoSeekButtons share the middle of the bottom row, well clear of Stop and volume.
func (r *renderer) videoSeekButtons() (backward, forward image.Rectangle) {
	w, h, gap := r.s(videoButtonW), r.s(videoButtonH), r.s(videoGap)
	y := r.h - r.s(112) + (r.s(112)-h)/2
	backward = image.Rect(r.w/2-w-gap/2, y, r.w/2-gap/2, y+h)
	return backward, backward.Add(image.Pt(w+gap, 0))
}

// videoTrackButtons are the sound and subtitle choices, in the bottom row's two gaps: the sound
// between Stop and -10s, the subtitles between +10s and quieter. Inside the strip, so what the panel
// blends over every frame stays the strip: a button anywhere else widens that to most of the screen,
// and blending it on every frame took a 24 fps video down to 6.
func (r *renderer) videoTrackButtons() (audio, subtitle image.Rectangle) {
	_, stop, quieter, _ := r.videoButtons()
	backward, forward := r.videoSeekButtons()
	w := r.s(videoButtonW)
	centered := func(left, right int) image.Rectangle {
		x := (left + right - w) / 2
		return image.Rect(x, stop.Min.Y, x+w, stop.Max.Y)
	}
	return centered(stop.Max.X, backward.Min.X), centered(forward.Max.X, quieter.Min.X)
}

// videoSeekTapped is whether a tap landed on -10s or +10s where they sit, drawn or not.
func (r *renderer) videoSeekTapped(at image.Point) videoTap {
	backward, forward := r.videoSeekButtons()
	switch {
	case at.In(backward):
		return videoTapBackward
	case at.In(forward):
		return videoTapForward
	}
	return videoTapNone
}

func (r *renderer) videoTimeline() image.Rectangle {
	return image.Rect(r.margin, r.h-r.s(176), r.w-r.margin, r.h-r.s(128))
}

// A timeline is deliberately tap-to-seek: Show swipes report their start, not a drag endpoint.
func (r *renderer) videoSeekPosition(x int, duration time.Duration) time.Duration {
	line := r.videoTimeline()
	return time.Duration(float64(duration) * float64(min(max(x-line.Min.X, 0), line.Dx()-1)) / float64(max(line.Dx()-1, 1)))
}

// videoPage draws the video page and says what of it goes over the picture.
func (r *renderer) videoPage(s scene) (over image.Rectangle) {
	r.zmu.Lock()
	r.videoZones = nil
	r.zmu.Unlock()
	if s.videoLive {
		draw.Draw(r.dst, r.dst.Rect, image.Transparent, image.Point{}, draw.Src)
	} else {
		draw.Draw(r.dst, r.dst.Rect, image.Black, image.Point{}, draw.Src)
		r.videoWords(s)
	}
	if s.videoControls && s.video.Active() {
		over = r.videoControls(s)
	}
	if s.videoLive && s.video.Caption != "" {
		over = over.Union(r.videoCaption(s))
	}
	if s.showVolume {
		r.volumeBar(s)
		over = over.Union(image.Rect(0, r.h-110, r.w, r.h))
	}
	return over
}

// videoWords are the page before the picture comes: what is loading, or why it could not be played.
func (r *renderer) videoWords(s scene) {
	v := s.video
	name := v.Title
	if name == "" {
		name = v.Host
	}
	if s.demo && name != "" {
		name = "A film"
	}
	head, sub := "Loading video…", name
	if !v.Active() && v.Err != "" {
		head, sub = "This video can't be played", v.Err
		if s.demo {
			sub = "Server returned 404 Not Found"
		}
	}
	cy := r.h/2 - r.s(40)
	if v.Active() || v.Err == "" {
		cy = r.h/2 - r.s(20)
	}
	r.text(r.title, head, (r.w-r.width(r.title, head))/2, cy, cream)
	room := r.w - 2*r.margin
	lines := r.wrapLines(r.small, sub, room)
	if len(lines) > 3 {
		lines = append(lines[:2], r.fit(r.small, lines[2]+" "+lines[3], room))
	}
	for i, l := range lines {
		r.text(r.small, l, (r.w-r.width(r.small, l))/2, cy+r.s(56)+i*r.s(42), dim)
	}
	if !v.Active() {
		hint := "Tap to go back"
		r.text(r.tiny, hint, (r.w-r.width(r.tiny, hint))/2, r.h-r.s(40), dim)
	}
}

// videoControls draws the strip of controls and remembers where they are, for a tap.
func (r *renderer) videoControls(s scene) image.Rectangle {
	strip := image.Rect(0, r.h-r.s(videoStripH), r.w, r.h)
	op := draw.Src // over a clear canvas: the panel blends it over the picture
	if !s.videoLive {
		op = draw.Over // over the page's own black, which the panel shows as it is
	}
	draw.Draw(r.dst, strip, image.NewUniform(videoStrip), image.Point{}, op)
	play, stop, quieter, louder := r.videoButtons()
	rad := float64(r.s(18))
	mark := "pause"
	if s.video.Phase == video.Paused {
		mark = "play"
	}
	for _, b := range []struct {
		rect image.Rectangle
		icon string
	}{{play, mark}, {stop, "stop"}, {quieter, "volume-minus"}, {louder, "volume-plus"}} {
		style := btnSecondary
		if b.rect == play {
			style = btnPrimary
		}
		fg := r.buttonFace(b.rect, rad, style)
		r.mdiIcon(b.icon, b.rect.Min.X+(b.rect.Dx()-r.s(videoIcon))/2, b.rect.Min.Y+(b.rect.Dy()-r.s(videoIcon))/2, videoIcon, fg)
	}
	// The title/time sit above the bottom row; the timeline's generous target does not touch it.
	name := s.video.Title
	if name == "" {
		name = s.video.Host
	}
	if s.demo && name != "" {
		name = "A film"
	}
	at := videoTime(s.video.Pos)
	if s.video.Dur > 0 {
		at += " / " + videoTime(s.video.Dur)
	}
	if s.video.Phase == video.Loading || !s.video.Frames {
		at = "Loading…"
	}
	line := r.videoTimeline()
	room := r.w/2 - r.margin
	if len(s.video.AudioTracks) > 1 || len(s.video.Subtitles) > 0 {
		room = r.w/3 - r.margin // the chosen sound and subtitles sit in the middle of the row
	}
	r.text(r.tiny, r.fit(r.tiny, name, room), r.margin, line.Max.Y+r.s(14), cream)
	r.text(r.tiny, at, r.w-r.margin-r.width(r.tiny, at), line.Max.Y+r.s(14), dim)
	zones := []image.Rectangle{play, stop, quieter, louder}
	if s.video.Dur > 0 {
		backward, forward := r.videoSeekButtons()
		for i, b := range []image.Rectangle{backward, forward} {
			fg := r.buttonFace(b, rad, btnSecondary)
			label := "-10s"
			if i == 1 {
				label = "+10s"
			}
			r.text(r.small, label, b.Min.X+(b.Dx()-r.width(r.small, label))/2, b.Min.Y+b.Dy()/2+r.s(10), fg)
		}
		bar := image.Rect(line.Min.X, line.Min.Y+r.s(20), line.Max.X, line.Min.Y+r.s(28))
		draw.Draw(r.dst, bar, image.NewUniform(dim), image.Point{}, draw.Over)
		filled := bar
		filled.Max.X = bar.Min.X + int(float64(bar.Dx())*float64(min(max(s.video.Pos, 0), s.video.Dur))/float64(s.video.Dur))
		draw.Draw(r.dst, filled, image.NewUniform(cream), image.Point{}, draw.Over)
		zones = append(zones, backward, forward, line)
	} else {
		zones = append(zones, image.Rectangle{}, image.Rectangle{}, image.Rectangle{})
	}
	r.videoTracks(s, rad, line, &zones)

	r.zmu.Lock()
	r.videoZones = zones
	r.zmu.Unlock()
	return strip
}

// videoTracks draws the sound and subtitle buttons when the video has choices, with what is chosen
// in the middle of the title row, and adds their zones (empty for a choice there is not) after the
// timeline's.
func (r *renderer) videoTracks(s scene, rad float64, line image.Rectangle, zones *[]image.Rectangle) {
	v := s.video
	audio, subtitle := r.videoTrackButtons()
	var drawn image.Rectangle
	var words []string
	if len(v.AudioTracks) > 1 {
		fg := r.buttonFace(audio, rad, btnSecondary)
		r.mdiIcon("account-voice", audio.Min.X+(audio.Dx()-r.s(videoIcon))/2, audio.Min.Y+(audio.Dy()-r.s(videoIcon))/2, videoIcon, fg)
		drawn = drawn.Union(audio)
		if v.Audio >= 0 && v.Audio < len(v.AudioTracks) {
			words = append(words, v.AudioTracks[v.Audio])
		}
	} else {
		audio = image.Rectangle{}
	}
	if len(v.Subtitles) > 0 {
		style := btnSecondary
		label := "Phụ đề: tắt"
		if v.Subtitle >= 0 && v.Subtitle < len(v.Subtitles) {
			style, label = btnPrimary, "Phụ đề: "+v.Subtitles[v.Subtitle]
		}
		fg := r.buttonFace(subtitle, rad, style)
		r.mdiIcon("subtitles", subtitle.Min.X+(subtitle.Dx()-r.s(videoIcon))/2, subtitle.Min.Y+(subtitle.Dy()-r.s(videoIcon))/2, videoIcon, fg)
		drawn = drawn.Union(subtitle)
		words = append(words, label)
	} else {
		subtitle = image.Rectangle{}
	}
	*zones = append(*zones, audio, subtitle)
	if drawn.Empty() {
		return
	}
	text := r.fit(r.tiny, strings.Join(words, " · "), r.w/3)
	r.text(r.tiny, text, (r.w-r.width(r.tiny, text))/2, line.Max.Y+r.s(14), cream)
}

// videoCaption draws the subtitle line due, at most two lines on a shade, above the controls when
// they are up and near the bottom when not, and says where.
func (r *renderer) videoCaption(s scene) image.Rectangle {
	room := r.w - 2*r.margin - r.s(32)
	lines := r.wrapLines(r.body, s.video.Caption, room)
	if len(lines) > 2 {
		lines = append(lines[:1], r.fit(r.body, strings.Join(lines[1:], " "), room))
	}
	lineH := r.s(46)
	bottom := r.h - r.s(28)
	if s.videoControls {
		bottom = r.h - r.s(videoStripH) - r.s(12)
	}
	widest := 0
	for _, l := range lines {
		widest = max(widest, r.width(r.body, l))
	}
	back := image.Rect((r.w-widest)/2-r.s(16), bottom-len(lines)*lineH-r.s(12), (r.w+widest)/2+r.s(16), bottom)
	draw.Draw(r.dst, back, image.NewUniform(videoStrip), image.Point{}, draw.Src)
	for i, l := range lines {
		r.text(r.body, l, (r.w-r.width(r.body, l))/2, back.Min.Y+r.s(12)+(i+1)*lineH-r.s(12), cream)
	}
	return back
}

// videoTapped is which control a tap landed on, as last drawn. A finger is a little wider than a
// button's edge.
func (r *renderer) videoTapped(at image.Point) videoTap {
	r.zmu.Lock()
	defer r.zmu.Unlock()
	for i, z := range r.videoZones {
		if at.In(z) {
			return videoTap(i + 1)
		}
	}
	return videoTapNone
}

// videoAskPage is the question about a DLNA video from an address that has not been allowed: who is
// asking and what it is, Not now on the left, Allow on the right, as the setup page's question is.
func (r *renderer) videoAskPage(s scene) {
	from, title := s.videoAsk.from, s.videoAsk.title
	if s.demo {
		from, title = "192.168.1.30", "A film"
	}
	r.text(r.title, "Video", r.margin, r.s(96), amber)
	r.text(r.body, r.fit(r.body, "Show a video from "+from+"?", r.w-2*r.margin), r.margin, r.s(176), cream)
	if title != "" {
		r.text(r.small, r.fit(r.small, title, r.w-2*r.margin), r.margin, r.s(224), dim)
	}
	r.text(r.tiny, "Allow remembers this address. Not now asks again later.", r.margin, r.s(268), dim)

	no, yes := r.actionHalves()
	rad := float64(r.s(actionRadius))
	mid := (no.Min.Y + no.Max.Y) / 2
	fg := r.buttonFace(no, rad, btnSecondary)
	r.text(r.body, "Not now", no.Min.X+(no.Dx()-r.width(r.body, "Not now"))/2, mid+r.s(14), fg)
	fg = r.buttonFace(yes, rad, btnPrimary)
	r.text(r.title, "Allow", yes.Min.X+(yes.Dx()-r.width(r.title, "Allow"))/2, mid+r.s(16), fg)
}
