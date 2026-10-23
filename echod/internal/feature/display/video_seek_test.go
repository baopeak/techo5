//go:build !dot && !spot

package display

import (
	"image"
	"testing"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/feature/video"
)

func TestVideoSeekTargetsAreDisjointAndVODOnly(t *testing.T) {
	for _, size := range []image.Point{{showWide, showHigh}, {show8Wide, show8High}} {
		r := newRenderer(image.NewRGBA(image.Rectangle{Max: size}))
		st := video.State{Phase: video.Playing, ID: 1, Frames: true, Dur: 100 * time.Second, Pos: 25 * time.Second}
		r.videoPage(scene{video: st, showVideo: true, videoLive: true, videoControls: true})
		play, stop, quieter, louder := r.videoButtons()
		backward, forward := r.videoSeekButtons()
		line := r.videoTimeline()
		zones := []image.Rectangle{play, stop, quieter, louder, backward, forward, line}
		for i, zone := range zones {
			for j := i + 1; j < len(zones); j++ {
				if !zone.Intersect(zones[j]).Empty() {
					t.Fatalf("%v: targets %d and %d overlap", size, i, j)
				}
			}
			center := zone.Min.Add(image.Pt(zone.Dx()/2, zone.Dy()/2))
			if got := r.videoTapped(center); got != videoTap(i+1) {
				t.Fatalf("%v: target %d at %v = %v", size, i, center, got)
			}
		}
		for _, tc := range []struct {
			x    int
			want time.Duration
		}{
			{line.Min.X - 20, 0}, {line.Min.X, 0}, {line.Max.X - 1, st.Dur}, {line.Max.X + 20, st.Dur},
		} {
			if got := r.videoSeekPosition(tc.x, st.Dur); got != tc.want {
				t.Fatalf("timeline at %d = %v, want %v", tc.x, got, tc.want)
			}
		}
		mid := r.videoSeekPosition((line.Min.X+line.Max.X)/2, st.Dur)
		if mid < 49*time.Second || mid > 51*time.Second {
			t.Fatalf("midpoint %v", mid)
		}
		st.Dur = 0
		r.videoPage(scene{video: st, showVideo: true, videoLive: true, videoControls: true})
		for _, zone := range []image.Rectangle{backward, forward, line} {
			if got := r.videoTapped(zone.Min.Add(image.Pt(zone.Dx()/2, zone.Dy()/2))); got != videoTapNone {
				t.Fatalf("live stream offered seek: %v", got)
			}
		}
	}
}

func TestNativeBrowserLeaseSurvivesSeekAndReturnsOnce(t *testing.T) {
	for _, ended := range []bool{false, true} {
		a := localApps{nativeID: 41, returnKind: "youtube"}
		for _, phase := range []video.Phase{video.Playing, video.Loading, video.Paused, video.Loading, video.Playing} {
			st := video.State{Phase: phase, ID: 41, LastID: 41, Origin: video.FromLocalApp}
			if kind := a.nativeReturn(st); kind != "" {
				t.Fatalf("seek phase %s returned the browser prematurely: %q", phase, kind)
			}
			if a.nativeID != 41 || a.returnKind != "youtube" {
				t.Fatalf("seek phase %s lost native ownership", phase)
			}
		}
		st := video.State{Phase: video.Idle, LastID: 41}
		if ended {
			st.Ended = 41
		}
		if kind := a.nativeReturn(st); kind != "youtube" {
			t.Fatalf("natural EOF=%v did not return browser: %q", ended, kind)
		}
		if kind := a.nativeReturn(st); kind != "" {
			t.Fatalf("natural EOF=%v returned browser twice: %q", ended, kind)
		}
	}
}
