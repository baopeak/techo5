//go:build !dot && !spot

package display

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/feature/media"
	"github.com/HuskerMinion/techo5/echod/internal/feature/video"
)

// Exercise a real child process and process group rather than a mocked launcher.
func TestLocalAppsLeaseThroughReap(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep unavailable")
	}
	var a localApps
	a.mu.Lock()
	err = a.startCommand(exec.Command(sleep, "30"), func() {})
	a.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	pid := a.cmd.Process.Pid
	t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
	if !a.active() {
		t.Fatal("lease must precede child execution")
	}
	a.request("youtube")
	a.mu.Lock()
	queued := a.requested
	a.mu.Unlock()
	if queued != "" {
		t.Fatal("overlapping launch was queued")
	}
	a.request("stop")
	deadline := time.Now().Add(5 * time.Second)
	for {
		owned, released := a.step(false, time.Now(), func() {})
		if released {
			if owned || a.active() {
				t.Fatal("lease retained after reap")
			}
			if err := syscall.Kill(-pid, 0); err != syscall.ESRCH {
				t.Fatalf("process group survived: %v", err)
			}
			break
		}
		if !owned {
			t.Fatal("lease lost before completion")
		}
		if time.Now().After(deadline) {
			t.Fatal("stop did not reap child")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestLocalAppsFailedStartReleasesLease(t *testing.T) {
	var a localApps
	a.mu.Lock()
	err := a.startCommand(exec.Command("/this-launcher-does-not-exist"), func() {})
	a.mu.Unlock()
	if err == nil || a.active() {
		t.Fatal("failed start retained the draw lease")
	}
}

func TestLocalAppsConcurrentRequests(t *testing.T) {
	var a localApps
	var wg sync.WaitGroup
	for range 64 {
		wg.Go(func() { a.request("web"); a.request("youtube"); _ = a.active() })
	}
	wg.Wait()
	a.request("stop")
	a.request("web")
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.requested != "stop" {
		t.Fatal("start displaced pending stop")
	}
}

func TestTrustedLauncherRejectsWritableAndSymlink(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("ownership validation requires root fixture")
	}
	dir := t.TempDir()
	path := dir + "/launcher"
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0777); err != nil {
		t.Fatal(err)
	}
	if trustedLauncher(path) == nil {
		t.Fatal("accepted writable launcher")
	}
	link := dir + "/link"
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if trustedLauncher(link) == nil {
		t.Fatal("accepted launcher symlink")
	}
}

func TestBrowserVideoRequestValidation(t *testing.T) {
	valid := `{"url":"https://video.example/stream?signature=private","audio_url":"https://audio.example/stream"}`
	req, err := readBrowserVideo(strings.NewReader(valid + "\n"))
	if err != nil || req.URL == "" || req.AudioURL == "" {
		t.Fatalf("valid separate streams rejected: %v", err)
	}
	if _, err := readBrowserVideo(strings.NewReader(`{"url":"https://video.example/stream"}` + "\n")); err != nil {
		t.Fatalf("muxed stream rejected: %v", err)
	}
	withTracks := `{"url":"https://video.example/stream","audio_url":"https://audio.example/orig",` +
		`"audio_tracks":[{"label":"Âm thanh gốc","url":"https://audio.example/orig"},{"label":"Lồng tiếng Việt","url":"https://audio.example/vi"}],` +
		`"subtitles":[{"label":"Tiếng Việt","vtt":"WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nxin chào\n"}]}`
	if req, err := readBrowserVideo(strings.NewReader(withTracks + "\n")); err != nil || len(req.AudioTracks) != 2 || len(req.Subtitles) != 1 {
		t.Fatalf("sounds and subtitles rejected: %+v %v", req, err)
	} else if tr := videoTracks(req.Subtitles); tr[0].Text == "" || tr[0].URL != "" {
		t.Fatalf("subtitles not carried as text: %+v", tr)
	}
	for name, input := range map[string]string{
		"subtitle address": `{"url":"https://video.example/stream","subtitles":[{"label":"x","url":"https://x.example/a.vtt"}]}`,
		"empty subtitle":   `{"url":"https://video.example/stream","subtitles":[{"label":"x"}]}`,
		"sound as text":    `{"url":"https://video.example/stream","audio_tracks":[{"label":"x","vtt":"WEBVTT"}]}`,
		"file sound":       `{"url":"https://video.example/stream","audio_tracks":[{"label":"x","url":"file:///etc/passwd"}]}`,
		"track field":      `{"url":"https://video.example/stream","audio_tracks":[{"label":"x","url":"https://a.example/","exec":1}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := readBrowserVideo(strings.NewReader(input + "\n")); err == nil {
				t.Fatal("invalid track accepted")
			}
		})
	}
	for name, input := range map[string]string{
		"empty":         "",
		"missing video": `{"audio_url":"https://audio.example/stream"}`,
		"unknown field": `{"url":"https://video.example/stream","command":"exec"}`,
		"second object": valid + `{}`,
		"trailing junk": valid + ` private`,
		"file video":    `{"url":"file:///etc/passwd"}`,
		"file audio":    `{"url":"https://video.example/stream","audio_url":"file:///etc/passwd"}`,
		"relative":      `{"url":"/stream"}`,
		"credentials":   `{"url":"https://secret:password@video.example/stream"}`,
		"fragment":      `{"url":"https://video.example/stream#private"}`,
		"oversized":     valid + strings.Repeat(" ", browserVideoLimit),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := readBrowserVideo(strings.NewReader(input + "\n")); err == nil {
				t.Fatal("invalid request accepted")
			} else if strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "password") {
				t.Fatal("request credentials leaked into error")
			}
		})
	}
	if _, err := readBrowserVideo(strings.NewReader(valid)); err == nil {
		t.Fatal("unterminated frame accepted")
	}
	// A later line cannot create a second request on this lease.
	if _, err := readBrowserVideo(strings.NewReader(valid + "\n{}\n")); err != nil {
		t.Fatalf("complete first frame rejected: %v", err)
	}
	exact := valid + strings.Repeat(" ", browserVideoLimit-len(valid)-1) + "\n"
	if _, err := readBrowserVideo(strings.NewReader(exact)); err != nil {
		t.Fatalf("request at size limit rejected: %v", err)
	}
	bounded := strings.NewReader(strings.Repeat(" ", browserVideoLimit*2))
	if _, err := readBrowserVideo(bounded); err == nil || bounded.Len() != browserVideoLimit-1 {
		t.Fatal("oversized request was not rejected at the read bound")
	}
}

// The real subprocess writes the same fd3 contract as the Python launcher and
// deliberately keeps its writer open: a complete frame must not wait for EOF.
// It stays alive until the lease owner terminates its process group.
func TestLocalAppsVideoChild(t *testing.T) {
	if os.Getenv("TECHO5_TEST_VIDEO_CHILD") != "1" {
		return
	}
	fd := os.NewFile(3, "browser-video")
	if fd == nil {
		os.Exit(2)
	}
	defer fd.Close()
	_, _ = io.WriteString(fd, `{"url":"https://video.example/stream","audio_url":"https://audio.example/stream"}`+"\n")
	_, _ = os.Stdout.Write([]byte{0, 0, 0, 0})
	time.Sleep(30 * time.Second)
	os.Exit(0)
}

func videoChildLease(t *testing.T) (*localApps, <-chan struct{}) {
	t.Helper()
	input, output, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	audio, pcm, err := os.Pipe()
	if err != nil {
		_ = input.Close()
		_ = output.Close()
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestLocalAppsVideoChild$")
	cmd.Env = []string{"TECHO5_TEST_VIDEO_CHILD=1", "TECHO5_VIDEO_FD=3"}
	cmd.ExtraFiles = []*os.File{output}
	cmd.Stdout = pcm
	woke := make(chan struct{}, 8)
	wake := func() {
		select {
		case woke <- struct{}{}:
		default:
		}
	}
	a := &localApps{kind: "youtube"}
	if err := a.startCommand(cmd, wake); err != nil {
		_ = input.Close()
		_ = output.Close()
		_ = audio.Close()
		_ = pcm.Close()
		t.Fatal(err)
	}
	_ = output.Close()
	_ = pcm.Close()
	a.audio = &browserPCM{File: audio}
	a.readVideo(input, wake)
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_ = input.Close()
		_ = audio.Close()
	})
	return a, woke
}

func awaitVideoRequest(t *testing.T, a *localApps, woke <-chan struct{}) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for len(a.videoReady) == 0 {
		select {
		case <-woke:
		case <-deadline.C:
			t.Fatal("child did not deliver fd3 request")
		}
	}
}

func releaseVideoLease(t *testing.T, a *localApps, priority bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		owned, released := a.step(priority, time.Now(), func() {})
		if released {
			if owned || a.active() {
				t.Fatal("release retained screen ownership")
			}
			if a.videoPipe != nil || a.videoReady != nil || a.audio != nil {
				t.Fatal("release retained browser pipes")
			}
			return
		}
		if !owned || time.Now().After(deadline) {
			t.Fatal("lease lost or child failed to reap")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestLocalAppsVideoHandoffWaitsForRelease(t *testing.T) {
	for _, priority := range []bool{false, true} {
		name := "explicit stop"
		if priority {
			name = "priority"
		}
		t.Run(name, func(t *testing.T) {
			a, woke := videoChildLease(t)
			awaitVideoRequest(t, a, woke)
			owned, released := a.step(false, time.Now(), func() {})
			if !owned || released || a.pending == nil || a.nativeID != 0 || a.stopping.IsZero() {
				t.Fatal("handoff did not retain the lease while stopping the browser")
			}
			pid := a.cmd.Process.Pid
			releaseVideoLease(t, a, false)
			if err := syscall.Kill(-pid, 0); err != syscall.ESRCH {
				t.Fatalf("released while browser group survived: %v", err)
			}
			if a.pending == nil || a.nativeID != 0 || a.returnKind != "youtube" {
				t.Fatal("native playback must remain staged until after framebuffer invalidation")
			}
			// Cancellation remains effective in the frame after lease release,
			// before a native renderer is allowed to start.
			if !priority {
				a.request("stop")
			}
			if owned, released := a.step(priority, time.Now(), func() {}); owned || released || a.pending != nil || a.returnKind != "" {
				t.Fatal("cancellation resurrected a staged handoff")
			}
		})
	}
}

func TestLocalAppsStopWinsVideoAndPCMReadiness(t *testing.T) {
	for _, priority := range []bool{false, true} {
		name := "explicit stop"
		if priority {
			name = "priority"
		}
		t.Run(name, func(t *testing.T) {
			a, woke := videoChildLease(t)
			awaitVideoRequest(t, a, woke)
			// Use actual child PCM to establish readiness, then queue stop in
			// the same frame. Neither ready channel may revive the session.
			if _, err := io.ReadFull(a.audio.File, a.audio.first[:]); err != nil {
				t.Fatal(err)
			}
			a.audioReady = make(chan error, 1)
			a.audioReady <- nil
			if !priority {
				a.request("stop")
			}
			if owned, released := a.step(priority, time.Now(), func() {}); !owned || released {
				t.Fatal("stop lost lease before reaping the child")
			}
			if a.audioReady != nil || a.videoReady != nil || a.audioTrack != (media.ReceivedTrack{}) {
				t.Fatal("stop accepted or retained ready media")
			}
			releaseVideoLease(t, a, priority)
			if a.pending != nil || a.nativeID != 0 || a.returnKind != "" {
				t.Fatal("stop accepted a ready video handoff")
			}
			if a.audioTrack != (media.ReceivedTrack{}) {
				t.Fatal("stop accepted ready PCM")
			}
		})
	}
}

func TestLocalAppsCleanupClosesBlockedVideoReader(t *testing.T) {
	a, _ := videoChildLease(t)
	// Replace the child's pipe with one whose writer never completes a frame.
	// Cleanup must unblock the reader without waiting for a request.
	_ = a.videoPipe.Close()
	input, output, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	a.readVideo(input, func() {})
	ready := a.videoReady
	a.request("stop")
	releaseVideoLease(t, a, false)
	select {
	case _, ok := <-ready:
		if ok {
			t.Fatal("cleanup produced a video request")
		}
	case <-time.After(time.Second):
		t.Fatal("cleanup left video reader blocked")
	}
}

func TestLocalAppsRejectedNativeStartReturnsBrowserOnce(t *testing.T) {
	for _, err := range []error{video.ErrOff, errors.New("decoder unavailable")} {
		a := localApps{returnKind: "youtube"}
		a.nativeStarted(0, err)
		if a.requested != "youtube" || a.returnKind != "" || a.nativeID != 0 || a.pending != nil {
			t.Fatal("synchronous rejection lost browser return or retained native retry")
		}
		// The queued return is an ordinary browser launch, not a second video
		// attempt, and explicit close can cancel it before the next frame.
		a.request("stop")
		if owned, released := a.step(false, time.Now(), func() {}); owned || released || a.requested != "" {
			t.Fatal("close failed to cancel browser return")
		}
	}
}

func TestLocalAppsNativeReplacementEndedBetweenFrames(t *testing.T) {
	a := localApps{nativeID: 41, returnKind: "youtube"}
	// A newer video both began and ended before the display next observed
	// state. Idle's zero current ID must not be mistaken for our completion.
	if kind := a.nativeReturn(video.State{Phase: video.Idle, LastID: 42, Ended: 42}); kind != "" {
		t.Fatal("browser reopened after an unrelated replacement")
	}
	if a.nativeID != 0 || a.returnKind != "" {
		t.Fatal("replacement retained stale browser-return ownership")
	}
}

func TestLocalAppsDLNAQuestionDoesNotReplaceNativeOwnership(t *testing.T) {
	for _, ownStillPlaying := range []bool{false, true} {
		a := localApps{nativeID: 41, returnKind: "youtube"}
		st := video.State{Phase: video.Asking, ID: 42, AskID: 42, LastID: 41}
		if ownStillPlaying {
			st.Phase, st.ID = video.Playing, 41
		}
		if kind := a.nativeReturn(st); kind != "" || a.nativeID != 41 || a.returnKind != "youtube" {
			t.Fatal("DLNA question discarded owned browser return")
		}
		// Declining the question does not begin a replacement. Once the
		// owned video is stopped/finished, its browser can return exactly once.
		if kind := a.nativeReturn(video.State{Phase: video.Idle, LastID: 41, Ended: 41}); kind != "youtube" {
			t.Fatal("declined DLNA question prevented browser return")
		}
		if a.nativeID != 0 || a.returnKind != "" {
			t.Fatal("completed return retained native ownership")
		}
	}
}
