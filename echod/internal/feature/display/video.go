//go:build !dot && !spot

package display

import (
	"context"
	"image"
	"log/slog"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/feature/media"
	"github.com/HuskerMinion/techo5/echod/internal/feature/phone"
	"github.com/HuskerMinion/techo5/echod/internal/feature/video"
	"github.com/HuskerMinion/techo5/echod/internal/hardware/touch"
)

// The video page (render_video.go): a video from feature/video, full screen over the clock, the
// dashboards and the deck. A call, a ring, the PIN pad, a browser asking for the setup page, the
// pairing and Wi-Fi pages, a camera, the settings and a voice turn go over it, and it pauses under
// them (video.Covered). A tap brings up its controls for a few seconds; a swipe down, or Stop, ends it.
//
// The picture never goes through the canvas. Once frames come, the frame loop draws only the
// controls there, and paintVideo hands each frame to the panel as the video's clock reaches it
// (screen.PresentFrame), blending the controls over it while they are up.

// videoCovered is whether something in the scene goes over the video, so it is not the page.
func videoCovered(s scene) bool {
	turn := s.phase == "listening" || s.phase == "thinking" || s.phase == "replying"
	return s.call.Phase != phone.Idle || s.ring.any() || s.pin.open || s.setupAsking || s.showVideoAsk || s.bt.Pairing ||
		s.showWifi || s.showCamera || s.showSheet || turn
}

// videoScene fills in the video's part of the scene, and tells the player whether it is covered.
func (d *Display) videoScene(s *scene, now time.Time) {
	st := video.Get().State()
	s.video = st
	if id, from, title, ok := video.Get().Asking(); ok {
		s.showVideoAsk, s.videoAsk = true, videoAsk{id: id, from: from, title: title}
	}
	// What draw() puts over the question (render.go) leaves it undrawn, and so unanswerable.
	drawnAsk := uint64(0)
	if s.showVideoAsk && s.call.Phase == phone.Idle && !s.ring.any() && !s.pin.open && !s.setupAsking {
		drawnAsk = s.videoAsk.id
	}
	d.vask.drawn(drawnAsk, now)
	d.mu.Lock()
	tried := d.videoTried
	d.mu.Unlock()
	failedRecently := st.Failed != 0 && st.Failed == tried && now.Sub(st.ErrAt) < videoFailShown
	up := st.Active() && st.Phase != video.Asking
	s.showVideo = (up || failedRecently) && !videoCovered(*s)
	video.Get().Covered(up && videoCovered(*s))
	d.mu.Lock()
	if up {
		d.videoTried = st.ID
	}
	controls := now.Before(d.videoUntil) || st.Phase == video.Paused || st.Phase == video.Loading || !st.Frames
	s.videoControls = controls
	s.videoLive = s.showVideo && up && st.Frames
	d.videoOnScreen = s.showVideo
	d.videoLive = s.videoLive
	d.mu.Unlock()
}

// videoNext is how long the frame loop may wait before it looks again while the video page is up.
func (d *Display) videoNext(now time.Time) time.Duration {
	d.mu.Lock()
	until := d.videoUntil
	d.mu.Unlock()
	wait := videoRecheck
	if video.Get().State().Subtitle >= 0 {
		wait = videoCaptionRecheck // a subtitle line changes within this of when it is due
	}
	if left := until.Sub(now); left > 0 && left < wait {
		wait = left + 10*time.Millisecond // the controls go when they are due to
	}
	return wait
}

// videoUp is whether the last frame drew the video page: then every finger is its.
func (d *Display) videoUp() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.videoOnScreen
}

// videoGesture is a finger on the video page.
func (d *Display) videoGesture(g touch.Gesture) {
	defer d.wake()
	if !video.Get().State().Active() {
		// The page saying a video could not be played: any touch puts it away.
		d.mu.Lock()
		d.videoTried = 0
		d.mu.Unlock()
		return
	}
	switch g.Kind {
	case touch.SwipeDown:
		slog.Info("screen: video stopped by a swipe")
		go video.Get().Stop()
	case touch.SwipeUp:
		d.showVideoControls()
	case touch.SwipeLeft, touch.SwipeRight:
		st := video.Get().State()
		by := -10 * time.Second
		if g.Kind == touch.SwipeRight {
			by = 10 * time.Second
		}
		video.Get().SkipIDAsync(st.ID, by)
		d.showVideoControls()
	case touch.Tap:
		d.mu.Lock()
		shown := time.Now().Before(d.videoUntil)
		d.mu.Unlock()
		st := video.Get().State()
		controls := shown || st.Phase == video.Paused || st.Phase == video.Loading || !st.Frames
		if !controls {
			d.showVideoControls()
			return
		}
		switch tapped := d.r.videoTapped(image.Pt(g.X, g.Y)); tapped {
		case videoTapPlay:
			go video.Get().Toggle()
			d.showVideoControls()
		case videoTapStop:
			slog.Info("screen: video stopped")
			go video.Get().Stop()
		case videoTapQuieter:
			go media.Get().Adjust(-1)
			d.showVideoControls()
		case videoTapLouder:
			go media.Get().Adjust(+1)
			d.showVideoControls()
		case videoTapBackward, videoTapForward:
			by := -10 * time.Second
			if tapped == videoTapForward {
				by = 10 * time.Second
			}
			video.Get().SkipIDAsync(st.ID, by)
			d.showVideoControls()
		case videoTapTimeline:
			at := d.r.videoSeekPosition(g.X, st.Dur)
			video.Get().SeekIDAsync(st.ID, at)
			d.showVideoControls()
		case videoTapAudio:
			if n := len(st.AudioTracks); n > 1 {
				next := (st.Audio + 1) % n
				slog.Info("screen: video sound switched", "to", next, "of", n)
				go func() {
					if err := video.Get().SwitchAudioID(st.ID, next); err != nil {
						slog.Warn("screen: video sound switch failed", "err", err)
					}
				}()
			}
			d.showVideoControls()
		case videoTapSubtitle:
			if n := len(st.Subtitles); n > 0 {
				next := st.Subtitle + 1
				if next >= n {
					next = -1
				}
				slog.Info("screen: video subtitles switched", "to", next, "of", n)
				go video.Get().SetSubtitleID(st.ID, next)
			}
			d.showVideoControls()
		default:
			// Off the controls: they go, and the picture is the whole screen again.
			d.mu.Lock()
			d.videoUntil = time.Time{}
			d.mu.Unlock()
		}
	}
}

func (d *Display) showVideoControls() {
	d.mu.Lock()
	d.videoUntil = time.Now().Add(videoControls)
	d.mu.Unlock()
}

// videoAskGesture is a finger on the question about a DLNA video: only the two answers answer it, and
// only the question drawn, once it has settled (askLatch).
func (d *Display) videoAskGesture(g touch.Gesture) {
	if g.Kind != touch.Tap || d.r == nil {
		return
	}
	id, ok := d.vask.answerable(time.Now())
	if !ok {
		return
	}
	if allow, answered := d.r.askTap(g.X, g.Y); answered {
		go video.Get().Answer(id, allow)
	}
}

// paintVideo shows the video's frames as they fall due (videoPainter.paint).
func (d *Display) paintVideo(ctx context.Context, wait time.Duration, over image.Rectangle) {
	d.vp.paint(ctx, d.dev, d.poke, wait, over)
}

// answerVideoShots hands whoever asked for a screenshot the video page as it is on the panel.
func (d *Display) answerVideoShots() {
	for {
		select {
		case ch := <-d.shots:
			ch <- d.vp.shot(d.dev, d.r.dst)
		default:
			return
		}
	}
}

// dropVideoFrame gives back the frame kept for the panel once the video page is gone.
func (d *Display) dropVideoFrame() { d.vp.drop() }
