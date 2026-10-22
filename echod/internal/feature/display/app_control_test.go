//go:build !dot && !spot

package display

import (
	"encoding/json"
	"net"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/feature/video"
	"github.com/HuskerMinion/techo5/echod/internal/feature/xiaozhi"
)

func TestAppControlStrictRequests(t *testing.T) {
	for _, input := range []string{
		`{"action":"open","app":"shell"}`,
		`{"action":"open","app":"browser","query":"private"}`,
		`{"action":"open","app":"web"}`,
		`{"action":"search","app":"browser","query":"valid"}`,
		`{"action":"search","app":"web","query":" x "}`,
		`{"action":"search","app":"web","query":"` + strings.Repeat("界", 501) + `"}`,
		`{"action":"status","app":"browser"}`,
		`{"action":"close","query":"private"}`,
		`{"action":"status","command":"private"}`,
		`{"action":"status"} {}`,
		`{"action":"status"} trailing`,
		`{"action":"missing"}`,
		"{\"action\":\"status\",\"query\":\"\xff\"}",
		strings.Repeat(" ", appControlLimit),
	} {
		if _, err := readAppControl(strings.NewReader(input + "\n")); err == nil {
			t.Fatalf("accepted malformed/schema-invalid request: %q", input)
		}
	}
	if _, err := readAppControl(strings.NewReader(`{"action":"status"}`)); err == nil {
		t.Fatal("accepted unterminated request")
	}
	for _, input := range []string{`{"action":"status"}`, `{"action":"close"}`, `{"action":"open","app":"netflix"}`, `{"action":"search","app":"web","query":"  âm nhạc Việt  "}`} {
		if _, err := readAppControl(strings.NewReader(input + "\n")); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAppControlProviderURLs(t *testing.T) {
	query := "âm nhạc Việt & mèo/猫?"
	for _, target := range []string{"web", "youtube", "netflix"} {
		in := appIntentFor(appControlRequest{Action: "search", App: target, Query: query})
		u, err := url.Parse(in.url)
		if err != nil {
			t.Fatal(err)
		}
		key := "q"
		if target == "youtube" {
			key = "search_query"
		}
		if u.Scheme != "https" || u.Query().Get(key) != query || len(u.Query()) != 1 {
			t.Fatalf("query encoding failed for %s", target)
		}
		if in.app == "web" {
			t.Fatal("web search did not normalize to browser state")
		}
	}
	for app, want := range map[string]string{"browser": "https://www.google.com/", "youtube": "https://www.youtube.com/", "netflix": "https://www.netflix.com/browse"} {
		if got := appIntentFor(appControlRequest{Action: "open", App: app}).url; got != want {
			t.Fatalf("%s default URL mismatch", app)
		}
	}
}

func queueApp(t *testing.T, a *localApps, app string) appControlResponse {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.nextRequest++
	return a.queueControl(appControlRequest{Action: "open", App: app})
}

func TestAppControlSpeakingDefersWithoutToolWait(t *testing.T) {
	a := &localApps{voiceBusy: true}
	result := make(chan appControlResponse, 1)
	go func() { result <- queueApp(t, a, "youtube") }()
	select {
	case response := <-result:
		if !response.OK || response.State != "queued" {
			t.Fatal("tool did not return queued")
		}
	case <-time.After(time.Second):
		t.Fatal("tool waited for speaking to end")
	}
	for range 3 {
		if owned, _ := a.step(false, time.Now(), func() {}); owned || a.intent == nil || a.control.State != "queued" {
			t.Fatal("speaking consumed accepted intent")
		}
	}
	// Use the same state transition as TTSStop, without actually launching a browser.
	a.mu.Lock()
	a.voiceBusy = false
	a.prepareControl(false)
	if a.intent != nil || a.requested != "youtube" || a.control.State != "starting" {
		t.Fatal("idle did not drain accepted intent")
	}
	a.mu.Unlock()
}

func TestAppControlLatestAndCancellations(t *testing.T) {
	for _, cancel := range []string{"close", "mute", "button", "safety", "normal app"} {
		t.Run(cancel, func(t *testing.T) {
			a := &localApps{voiceBusy: true}
			first := queueApp(t, a, "youtube")
			last := queueApp(t, a, "netflix")
			if last.RequestID <= first.RequestID || a.intent.app != "netflix" {
				t.Fatal("latest intent did not win")
			}
			switch cancel {
			case "close":
				a.controlRequest(appControlRequest{Action: "close"})
			case "mute", "button":
				a.cancelIntent()
			case "safety":
				a.step(true, time.Now(), func() {})
			case "normal app":
				a.request("web")
			}
			if a.intent != nil {
				t.Fatal("cancellation retained old intent")
			}
			if cancel != "normal app" {
				a.step(false, time.Now(), func() {})
				if a.requested != "" || a.control.State != "closed" {
					t.Fatal("canceled request can relaunch")
				}
			}
		})
	}
}

func TestAppControlMissingLauncherIsError(t *testing.T) {
	if trustedLauncher(browserLauncher) == nil {
		t.Skip("real launcher installed")
	}
	var a localApps
	response := a.controlRequest(appControlRequest{Action: "open", App: "browser"})
	if response.OK || response.State != "error" || response.Error == "" || a.intent != nil {
		t.Fatal("missing launcher reported success")
	}
	if status := a.controlRequest(appControlRequest{Action: "status"}); status.State != "error" || status.OK {
		t.Fatal("error status was lost")
	}
}

func TestBrowserReadyExactFrame(t *testing.T) {
	for _, input := range []string{"", "ready", "ready\nextra", "other\n", "ready\n\n"} {
		if readBrowserReady(strings.NewReader(input)) == nil {
			t.Fatal("accepted invalid readiness frame")
		}
	}
	if err := readBrowserReady(strings.NewReader("ready\n")); err != nil {
		t.Fatal(err)
	}
}

func TestAppControlGenerationAndReadinessFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "ready", true: "EOF"}[fail], func(t *testing.T) {
			a, _ := videoChildLease(t)
			a.videoReady = nil
			a.control = appControlResponse{OK: true, State: "starting", App: "youtube"}
			a.generation = 2
			a.browserReady = make(chan browserReadyResult, 2)
			a.browserReady <- browserReadyResult{generation: 1}
			a.step(false, time.Now(), func() {})
			if a.control.State != "starting" || a.browserReady == nil {
				t.Fatal("retired generation affected current readiness")
			}
			result := browserReadyResult{generation: 2}
			if fail {
				result.err = readBrowserReady(strings.NewReader(""))
			}
			a.browserReady <- result
			a.step(false, time.Now(), func() {})
			want := "open"
			if fail {
				want = "error"
			}
			if a.control.State != want || (fail && a.control.OK) {
				t.Fatalf("readiness state %s, want %s", a.control.State, want)
			}
			a.request("stop")
			releaseVideoLease(t, a, false)
		})
	}
}

func TestAppControlReadyCleanupUnblocksReader(t *testing.T) {
	input, output, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	var a localApps
	a.readReady(input, func() {})
	ready := a.browserReady
	a.clearReady()
	select {
	case result := <-ready:
		if result.err == nil {
			t.Fatal("cleanup acknowledged readiness")
		}
	case <-time.After(time.Second):
		t.Fatal("cleanup left readiness reader blocked")
	}
}

func TestAppControlUnixAuthorizationAndProtocol(t *testing.T) {
	path := t.TempDir() + "/control.sock"
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	for _, authorized := range []bool{false, true} {
		done := make(chan struct{})
		called := false
		go func() {
			defer close(done)
			conn, err := listener.AcceptUnix()
			if err != nil {
				return
			}
			if os.Getuid() == 0 && !appPeerAllowed(conn) {
				t.Error("root diagnostic peer denied")
			}
			serveAppConnection(conn, authorized, func(req appControlRequest) appControlResponse {
				called = true
				return appControlResponse{OK: true, State: "closed"}
			})
		}()
		client, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		client.SetDeadline(time.Now().Add(time.Second))
		if authorized {
			_, _ = client.Write([]byte("{\"action\":\"status\"}\n"))
		}
		var response appControlResponse
		if err := json.NewDecoder(client).Decode(&response); err != nil {
			t.Fatal(err)
		}
		client.Close()
		<-done
		if response.OK != authorized || called != authorized {
			t.Fatal("peer authorization bypassed")
		}
	}
}

func TestAppControlActualXiaozhiStopCancelsQueuedAndStarting(t *testing.T) {
	t.Run("queued", func(t *testing.T) {
		a := &localApps{voiceBusy: true}
		queueApp(t, a, "youtube")
		(&xiaozhi.Feature{}).Stop()
		a.step(false, time.Now(), func() {})
		if a.intent != nil || a.control.State != "closed" || a.leased || a.requested != "" {
			t.Fatal("explicit Xiaozhi stop retained a queued launch")
		}
		a.voiceBusy = false
		a.step(false, time.Now(), func() {})
		if a.leased || a.control.State != "closed" {
			t.Fatal("old request relaunched after stop")
		}
	})
	t.Run("starting", func(t *testing.T) {
		a, _ := videoChildLease(t)
		queueApp(t, a, "youtube")
		a.intent = nil
		a.control.State = "starting"
		a.generation = 1
		a.browserReady = make(chan browserReadyResult, 1)
		a.browserReady <- browserReadyResult{generation: 1}
		(&xiaozhi.Feature{}).Stop()
		a.step(false, time.Now(), func() {})
		if a.stopping.IsZero() || a.control.State == "open" || a.browserReady != nil {
			t.Fatal("explicit Xiaozhi stop lost to simultaneous browser readiness")
		}
		releaseVideoLease(t, a, false)
		if a.control.State != "closed" || a.intent != nil {
			t.Fatal("stopped starting app was retained")
		}
	})
}

func TestAppControlReplacementWaitsForOldLeaseReap(t *testing.T) {
	a, _ := videoChildLease(t)
	old := a.cmd
	queueApp(t, a, "youtube")
	latest := queueApp(t, a, "netflix")
	a.step(false, time.Now(), func() {})
	if a.cmd != old || a.stopping.IsZero() || a.intent == nil || a.control.State != "queued" {
		t.Fatal("replacement did not stop the old writer while retaining latest intent")
	}
	releaseVideoLease(t, a, false)
	if a.cmd != nil || a.intent == nil || a.intent.id != latest.RequestID {
		t.Fatal("old writer was not reaped before replacement became eligible")
	}
	a.mu.Lock()
	a.prepareControl(false)
	if a.requested != "web" || a.launchURL != "https://www.netflix.com/browse" || a.control.State != "starting" {
		t.Fatal("latest typed replacement was lost during process reaping")
	}
	a.mu.Unlock()
}

func TestAppControlMuteWhileStartingBeatsReady(t *testing.T) {
	a, _ := videoChildLease(t)
	queueApp(t, a, "youtube")
	a.intent = nil
	a.control.State = "starting"
	a.generation = 8
	a.browserReady = make(chan browserReadyResult, 1)
	a.browserReady <- browserReadyResult{generation: 8}
	// Same callback used by the Run-owned mute listener, after the queued
	// intent has already been consumed by launcher creation.
	a.cancelIntent()
	a.step(false, time.Now(), func() {})
	if a.stopping.IsZero() || a.control.State == "open" || a.browserReady != nil {
		t.Fatal("mute accepted stale browser readiness during startup")
	}
	releaseVideoLease(t, a, false)
	if a.control.State != "closed" {
		t.Fatal("mute failed to finish startup cancellation")
	}
}

func TestLocalAppsSafetyDropsBusyRetainedButtonAndNativeReturn(t *testing.T) {
	for _, source := range []string{"button", "native return"} {
		t.Run(source, func(t *testing.T) {
			a := &localApps{voiceBusy: true}
			if source == "button" {
				a.request("web")
			} else {
				a.returnKind = "youtube"
				a.returnURL = "https://www.youtube.com/results?search_query=music"
				a.nativeStarted(0, os.ErrPermission)
			}
			a.step(false, time.Now(), func() {})
			if a.requested == "" {
				t.Fatal("fixture did not retain busy launch")
			}
			a.step(true, time.Now(), func() {})
			if a.requested != "" || a.launchURL != "" {
				t.Fatal("safety retained deferred launch")
			}
			a.voiceBusy = false
			a.step(false, time.Now(), func() {})
			if a.leased || a.requested != "" {
				t.Fatal("dismissed safety resurrected old launch")
			}
		})
	}
}

func TestAppControlNativeCloseWaitsForDecoderCompletion(t *testing.T) {
	done := make(chan struct{})
	a := &localApps{nativeID: 41, nativeDone: done}
	response := a.controlRequest(appControlRequest{Action: "close"})
	if response.State == "closed" {
		t.Fatal("close prematurely completed native ownership")
	}
	a.step(false, time.Now(), func() {})
	if a.control.State == "closed" || a.nativeID == 0 || !a.shutdownPending() {
		t.Fatal("removed native session was mistaken for reaped decoder")
	}
	close(done)
	a.step(false, time.Now(), func() {})
	if a.control.State != "closed" || a.nativeID != 0 || a.shutdownPending() {
		t.Fatal("completed native teardown did not finish close")
	}
}

func TestLocalAppsNativeReturnWaitsForDecoderCompletion(t *testing.T) {
	done := make(chan struct{})
	woke := make(chan struct{}, 1)
	a := &localApps{
		nativeID: 41, returnKind: "youtube",
		returnURL: "https://www.youtube.com/results?search_query=nh%E1%BA%A1c",
	}
	completed := func(id uint64) <-chan struct{} {
		if id != 41 {
			t.Fatalf("completion requested for wrong native owner: %d", id)
		}
		return done
	}
	wake := func() { woke <- struct{}{} }
	idle := video.State{Phase: video.Idle, LastID: 41, Ended: 41}
	if request := a.nativeReturnAfter(idle, completed, wake); request != "" {
		t.Fatal("public native idle relaunched browser before decoder completion")
	}
	if a.nativeID != 41 || a.returnKind != "youtube" || a.returnURL == "" || a.launchURL != "" {
		t.Fatal("waiting for native teardown consumed browser return destination")
	}
	// This is also the early-frame barrier used on the next iteration: it
	// must not consume a natural browser return as though it were close.
	if a.returnKind == "" {
		t.Fatal("natural return lost to explicit-close barrier")
	}
	close(done)
	select {
	case <-woke:
	case <-time.After(time.Second):
		t.Fatal("native completion did not wake display loop")
	}
	if request := a.nativeReturnAfter(idle, completed, wake); request != "youtube" {
		t.Fatal("completed native decoder did not release browser return")
	}
	if a.nativeID != 0 || a.nativeDone != nil || a.returnKind != "" || a.returnURL != "" || a.launchURL == "" {
		t.Fatal("native completion failed to transfer return destination exactly once")
	}
}
