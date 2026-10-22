//go:build !dot && !spot

package display

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/HuskerMinion/techo5/echod/internal/feature/mute"
	"github.com/HuskerMinion/techo5/echod/internal/feature/xiaozhi"
	"github.com/HuskerMinion/techo5/echod/internal/hardware/buttons"
)

const appControlPath = "/run/techo5-app-control/control.sock"
const appControlLimit = 4096
const appControlGroup = 65532

type appControlRequest struct {
	Action string `json:"action"`
	App    string `json:"app,omitempty"`
	Query  string `json:"query,omitempty"`
}

type appControlResponse struct {
	OK        bool   `json:"ok"`
	State     string `json:"state"`
	App       string `json:"app,omitempty"`
	RequestID uint64 `json:"request_id,omitempty"`
	Error     string `json:"error,omitempty"`
}

type appIntent struct {
	app string
	url string
	id  uint64
}

func readAppControl(r io.Reader) (appControlRequest, error) {
	var req appControlRequest
	data, err := bufio.NewReader(io.LimitReader(r, appControlLimit+1)).ReadBytes('\n')
	if err != nil || len(data) > appControlLimit || !utf8.Valid(data) {
		return req, errors.New("send one valid UTF-8 JSON line of at most 4096 bytes")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil || dec.Decode(new(any)) != io.EOF {
		return appControlRequest{}, errors.New("invalid app control request schema")
	}
	switch req.Action {
	case "open":
		if (req.App != "browser" && req.App != "youtube" && req.App != "netflix") || req.Query != "" {
			return req, errors.New("open requires browser, youtube or netflix without query")
		}
	case "search":
		req.Query = strings.TrimSpace(req.Query)
		n := utf8.RuneCountInString(req.Query)
		if (req.App != "web" && req.App != "youtube" && req.App != "netflix") || n < 2 || n > 500 {
			return req, errors.New("search requires web, youtube or netflix and 2..500 query characters")
		}
	case "close", "status":
		if req.App != "" || req.Query != "" {
			return req, errors.New("close and status do not accept app or query")
		}
	default:
		return req, errors.New("action must be open, search, close or status")
	}
	return req, nil
}

func appIntentFor(req appControlRequest) appIntent {
	in := appIntent{app: req.App}
	if in.app == "web" {
		in.app = "browser"
	}
	switch in.app {
	case "browser":
		in.url = "https://www.google.com/"
		if req.Action == "search" {
			in.url += "search?q=" + url.QueryEscape(req.Query)
		}
	case "youtube":
		in.url = "https://www.youtube.com/"
		if req.Action == "search" {
			in.url += "results?search_query=" + url.QueryEscape(req.Query)
		}
	case "netflix":
		in.url = "https://www.netflix.com/browse"
		if req.Action == "search" {
			in.url = "https://www.netflix.com/search?q=" + url.QueryEscape(req.Query)
		}
	}
	return in
}

func (a *localApps) controlStatus() appControlResponse {
	if a.control.State == "" {
		return appControlResponse{OK: true, State: "closed"}
	}
	return a.control
}

func (a *localApps) controlRequest(req appControlRequest) appControlResponse {
	a.mu.Lock()
	defer a.mu.Unlock()
	if req.Action == "status" {
		return a.controlStatus()
	}
	a.nextRequest++
	if req.Action == "close" {
		a.intent = nil
		xiaozhi.SetAppHandoffPending(false)
		a.requested = "stop"
		a.control = appControlResponse{OK: true, State: "starting", RequestID: a.nextRequest}
		if !a.leased && a.nativeID == 0 && a.pending == nil {
			a.control.State = "closed"
		}
		return a.control
	}
	if trustedLauncher(browserLauncher) != nil {
		a.intent = nil
		xiaozhi.SetAppHandoffPending(false)
		a.control = appControlResponse{State: "error", RequestID: a.nextRequest, Error: "browser launcher unavailable; install the on-device browser runtime"}
		return a.control
	}
	return a.queueControl(req)
}

// queueControl is called under mu after validating launcher capability.
func (a *localApps) queueControl(req appControlRequest) appControlResponse {
	in := appIntentFor(req)
	in.id = a.nextRequest
	a.intent = &in
	a.cancelGeneration = xiaozhi.CancellationGeneration()
	xiaozhi.SetAppHandoffPending(true)
	a.control = appControlResponse{OK: true, State: "queued", App: in.app, RequestID: in.id}
	return a.control
}

func (a *localApps) controlError(message string) {
	xiaozhi.SetAppHandoffPending(false)
	a.control.OK, a.control.State, a.control.Error = false, "error", message
}

// Comparing a monotonic atomic signal avoids taking Xiaozhi's screen/control
// locks from the frame loop or confusing normal TTS completion with an abort.
func (a *localApps) retireCanceledControl(generation uint64) bool {
	if a.control.RequestID == 0 || (a.control.State != "queued" && a.control.State != "starting") || generation == a.cancelGeneration {
		return false
	}
	a.intent = nil
	xiaozhi.SetAppHandoffPending(false)
	a.requested = "stop"
	a.control.State = "closed"
	if a.leased || a.nativeID != 0 {
		a.control.State = "starting"
	} else {
		a.control.App = ""
	}
	a.cancelGeneration = generation
	return true
}
func (a *localApps) controlCanceled() bool {
	return a.retireCanceledControl(xiaozhi.CancellationGeneration())
}

// Called only under mu by the frame loop. Busy voice defers an intent; safety
// screens retire it permanently, so their dismissal cannot resurrect an app.
func (a *localApps) prepareControl(priority bool) {
	a.controlCanceled()
	if priority {
		a.intent = nil
		a.requested, a.launchURL = "stop", ""
		xiaozhi.SetAppHandoffPending(false)
		if a.control.State != "error" {
			a.control.State = "closed"
			if a.leased || a.nativeID != 0 {
				a.control.State = "starting"
			} else {
				a.control.App = ""
			}
		}
		return
	}
	if a.intent == nil {
		return
	}
	if a.leased || a.nativeID != 0 || a.pending != nil {
		a.requested = "stop"
		return
	}
	if a.voiceBusy {
		return
	}
	in := a.intent
	a.intent = nil
	a.launchURL = in.url
	a.requested = "web"
	if in.app == "youtube" {
		a.requested = "youtube"
	}
	a.control = appControlResponse{OK: true, State: "starting", App: in.app, RequestID: in.id}
}

func (a *localApps) cancelIntent() {
	a.mu.Lock()
	if a.intent != nil || (a.control.RequestID != 0 && a.control.State == "starting") {
		a.intent = nil
		xiaozhi.SetAppHandoffPending(false)
		a.requested = "stop"
		a.control.State, a.control.App = "closed", ""
		if a.leased || a.nativeID != 0 {
			a.control.State = "starting"
		}
	}
	a.mu.Unlock()
}

func appPeerAllowed(conn *net.UnixConn) bool {
	raw, err := conn.SyscallConn()
	if err != nil {
		return false
	}
	allowed := false
	if raw.Control(func(fd uintptr) {
		cred, err := syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
		allowed = err == nil && (cred.Uid == 0 || cred.Uid == 65534)
	}) != nil {
		return false
	}
	return allowed
}

func serveAppConnection(conn *net.UnixConn, allowed bool, handle func(appControlRequest) appControlResponse) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	response := appControlResponse{State: "error", Error: "app control peer is not authorized"}
	if allowed {
		req, err := readAppControl(conn)
		if err != nil {
			response.Error = err.Error()
		} else {
			response = handle(req)
		}
	}
	_ = json.NewEncoder(conn).Encode(response)
}

// The listener, accepted requests and event subscriptions are all owned by Run.
// Closing waits for handlers before display's deferred process cleanup begins.
func (d *Display) runAppControl(ctx context.Context) (func(), error) {
	dir := filepath.Dir(appControlPath)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return nil, err
	}
	st, err := os.Lstat(dir)
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("unsafe app control directory")
	}
	if err := os.Chown(dir, 0, appControlGroup); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0750); err != nil {
		return nil, err
	}
	if st, err := os.Lstat(appControlPath); err == nil {
		if st.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("app control path is not a socket")
		}
		if err := os.Remove(appControlPath); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: appControlPath, Net: "unix"})
	if err != nil {
		return nil, err
	}
	if err := os.Chown(appControlPath, 0, appControlGroup); err != nil {
		listener.Close()
		return nil, err
	}
	if err := os.Chmod(appControlPath, 0660); err != nil {
		listener.Close()
		return nil, err
	}
	cancelMute := mute.Get().Changed.Listen(func(on bool) {
		if on {
			d.apps.cancelIntent()
			d.wake()
		}
	})
	cancelButtons := buttons.Get().Events.Listen(func(e buttons.Event) {
		if e.Name == buttons.Action {
			d.apps.cancelIntent()
			d.wake()
		}
	})
	stop := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			listener.Close()
		case <-stop:
		}
	}()
	go func() {
		defer close(finished)
		for {
			conn, err := listener.AcceptUnix()
			if err != nil {
				return
			}
			// A bounded serial handler avoids an untrusted client creating
			// unlimited goroutines. The socket deadline bounds shutdown.
			serveAppConnection(conn, appPeerAllowed(conn), func(req appControlRequest) appControlResponse {
				response := d.apps.controlRequest(req)
				d.wake()
				return response
			})
		}
	}()
	return func() { cancelMute(); cancelButtons(); close(stop); listener.Close(); <-finished }, nil
}

// A removed video session may still own a decoder. The completion channel, not
// the public idle state or StopID return, is the native framebuffer barrier.
func (a *localApps) finishNativeStop() bool {
	if a.nativeDone == nil {
		return true
	}
	select {
	case <-a.nativeDone:
		a.nativeDone, a.nativeID = nil, 0
		return true
	default:
		if a.intent == nil && a.control.State != "error" {
			a.control.State = "starting"
		}
		return false
	}
}

func (a *localApps) shutdownPending() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.leased || a.nativeDone != nil || a.nativeID != 0
}
