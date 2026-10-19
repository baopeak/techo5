package xiaozhi

import (
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/HuskerMinion/techo5/echod/internal/config"
)

func TestSilentCloudAfterManualStopReconnectsAndReleasesThinking(t *testing.T) {
	old := firstResponseWait
	firstResponseWait = 100 * time.Millisecond
	t.Cleanup(func() { firstResponseWait = old })
	cl := connect(t)
	_, opened := cl.ask(t, `{"cmd":"listen","state":"start","mode":"manual"}`)
	cl.f.mu.Lock()
	original := cl.f.sess
	cl.f.mu.Unlock()
	cl.speech(t, 25)
	cl.hush(t, 65)
	cl.end(t, opened.ID)
	cl.waitHeld(t, false, "capture ended before waiting for the cloud")
	waitResponseRecovery(t, cl, original)
}

func waitResponseRecovery(t *testing.T, cl *client, original *Session) {
	t.Helper()

	deadline := time.Now().Add(9 * time.Second)
	for time.Now().Before(deadline) {
		cl.f.mu.Lock()
		next := cl.f.sess
		cl.f.mu.Unlock()
		screenMu.Lock()
		phase := current.Phase
		screenMu.Unlock()
		if next != nil && next != original && phase == "idle" {
			if held, _ := cl.holding(); held || cl.f.Listening() {
				t.Fatal("silent-response recovery reopened the microphone")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("cloud sent no response after listen stop: Thinking did not clear and the session did not reconnect")
}

func TestResponseWaitTracksRequestProgressNotTransportTraffic(t *testing.T) {
	start := time.Unix(100, 0)
	w := responseWait{turn: 1}
	w.submit(start)
	w.observe(Event{Type: TypePing}, start.Add(firstResponseWait))
	if why := w.expired(start.Add(firstResponseWait)); why == "" {
		t.Fatal("transport pings kept an unrecognized request waiting")
	}

	w = responseWait{turn: 2}
	w.observe(Event{Type: TypeSTT, Text: "Kể thêm về Đà Lạt"}, start)
	w.submit(start.Add(time.Second))
	if why := w.expired(start.Add(firstResponseWait + 2*time.Second)); why != "" {
		t.Fatalf("recognition did not preserve time for model/tool work: %s", why)
	}
	w.observe(Event{Type: TypeLLM, Text: "a response"}, start.Add(80*time.Second))
	if why := w.expired(start.Add(100 * time.Second)); why != "" {
		t.Fatalf("real answer progress was ignored: %s", why)
	}
	if why := w.expired(start.Add(171 * time.Second)); why == "" {
		t.Fatal("recognized request with no further progress waited indefinitely")
	}

	for _, event := range []Event{{Type: TypeTTS, Audio: []byte{1}}, {Type: TypeTTS, State: TTSStop}} {
		w = responseWait{turn: 3}
		w.submit(start)
		w.observe(Event{Type: TypeTTS, State: TTSStart}, start)
		w.observe(event, start.Add(time.Second))
		if why := w.expired(start.Add(time.Hour)); why != "" {
			t.Fatalf("audio or a completed empty answer remained pending: %s", why)
		}
	}
	w = responseWait{turn: 4}
	w.submit(start)
	w.observe(Event{Type: TypeTTS, State: TTSStop}, start.Add(time.Second))
	if why := w.expired(start.Add(firstResponseWait)); why == "" {
		t.Fatal("an old answer's trailing stop completed a new request without a TTS start")
	}
	w = responseWait{turn: 4}
	if why := w.expired(start.Add(time.Hour)); why != "" {
		t.Fatal("an idle or still-recording turn was treated as a submitted request")
	}
}

func TestNewListeningTurnAndExplicitStopRetireOldResponseWait(t *testing.T) {
	old := firstResponseWait
	firstResponseWait = 100 * time.Millisecond
	t.Cleanup(func() { firstResponseWait = old })
	cl := connect(t)
	_, opened := cl.ask(t, `{"cmd":"listen","state":"start","mode":"manual"}`)
	cl.speech(t, 25)
	cl.hush(t, 65)
	cl.end(t, opened.ID)
	cl.f.mu.Lock()
	original := cl.f.sess
	cl.f.mu.Unlock()

	if err := cl.f.Listen(ListenAuto); err != nil {
		t.Fatalf("new explicit listen after a pending answer: %v", err)
	}
	cl.waitHeld(t, true, "new listening owns the mic")
	// Cross a real watchdog tick. The old pending answer must not cancel
	// this active capture even though its short test deadline has elapsed.
	time.Sleep(1200 * time.Millisecond)
	cl.f.mu.Lock()
	same := cl.f.sess == original
	cl.f.mu.Unlock()
	if !same || !cl.f.Listening() {
		t.Fatal("the previous response deadline canceled a newer listening turn")
	}
	cl.f.Stop()
	cl.waitHeld(t, false, "explicit stop releases the new capture")
	time.Sleep(1200 * time.Millisecond)
	cl.f.mu.Lock()
	same = cl.f.sess == original
	cl.f.mu.Unlock()
	if !same {
		t.Fatal("explicit stop left a response timeout armed on a healthy idle connection")
	}
}

func TestInterruptedAnswerPacketsDoNotDisableNextRequestTimeout(t *testing.T) {
	old := firstResponseWait
	firstResponseWait = 100 * time.Millisecond
	t.Cleanup(func() { firstResponseWait = old })
	cl := connect(t)
	if err := config.Set().Microphone().DeviceEnds(true); err != nil {
		t.Fatal(err)
	}
	down := cl.downlink(t)
	down.started()
	if err := cl.f.Wake(); err != nil {
		t.Fatal(err)
	}
	cl.waitHeld(t, true, "new wake interrupts the old answer")
	cl.f.mu.Lock()
	original, opened := cl.f.sess, cl.f.turn.id
	cl.f.mu.Unlock()
	cl.cloud.mu.Lock()
	conn := cl.cloud.conn
	cl.cloud.mu.Unlock()
	// The old answer can have an Opus packet and its stop still in flight
	// after abort. The hushed downlink must discard them, not acknowledge B.
	pkt := answer(t, defaultDownlinkRate, 60, 1, 440)[0]
	if err := conn.WriteMessage(websocket.BinaryMessage, pkt); err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteJSON(Event{Type: TypeTTS, State: TTSStop}); err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteJSON(Event{Type: TypePing}); err != nil {
		t.Fatal(err)
	}
	for {
		r, err := cl.term.next()
		if err != nil {
			t.Fatal(err)
		}
		if r.Msg != nil && r.Msg.Type == TypePing {
			break
		}
	}
	down.mu.Lock()
	dropped := down.dropped
	down.mu.Unlock()
	if dropped == 0 {
		t.Fatal("interrupted answer's packet was not discarded")
	}
	cl.speech(t, 25)
	cl.hush(t, 65)
	cl.end(t, opened)
	waitResponseRecovery(t, cl, original)
}

func TestStopWithNoCaptureDoesNotDisarmSilentResponseRecovery(t *testing.T) {
	old := firstResponseWait
	firstResponseWait = 100 * time.Millisecond
	t.Cleanup(func() { firstResponseWait = old })
	cl := connect(t)
	_, opened := cl.ask(t, `{"cmd":"listen","state":"start","mode":"manual"}`)
	cl.f.mu.Lock()
	original := cl.f.sess
	cl.f.mu.Unlock()
	cl.speech(t, 25)
	cl.hush(t, 65)
	cl.end(t, opened.ID)
	cl.f.Stop()
	waitResponseRecovery(t, cl, original)
}

func TestEarlyAutoAnswerCompletesWhileCaptureIsStillClosing(t *testing.T) {
	restore(t)
	f := build()
	b := newBench(t, defaultDownlinkRate)
	t.Cleanup(b.d.close)
	sess := &Session{stats: Stats{Since: time.Now()}}
	up, err := newUplink(sess)
	if err != nil {
		t.Fatal(err)
	}
	// Keep the auto capture installed deliberately: STT has canceled it,
	// but its goroutine has not reached closeTurn yet.
	turn := &turn{id: 1, sess: sess, up: up, mode: ListenAuto, at: time.Now(), cancel: func() {}}
	f.sess, f.down, f.turn = sess, b.d, turn
	f.response = responseWait{turn: turn.id}
	f.onEvent(Event{Type: TypeSTT, Text: "Xin chào"}, b.d)
	if !turn.serverDone.Load() {
		t.Fatal("automatic transcript did not end capture on the server")
	}
	for _, pkt := range answer(t, defaultDownlinkRate, 60, 8, 440) {
		f.onEvent(Event{Type: TypeTTS, Audio: pkt}, b.d)
	}
	f.onEvent(Event{Type: TypeTTS, State: TTSStop}, b.d)
	f.closeTurn(turn, "canceled")
	out := b.drain(t)
	if lo, hi := span(out); lo < 0 || hi <= lo {
		t.Fatal("early answer did not render audible PCM")
	}
	if why, _ := f.expireResponse(sess, b.d, time.Now().Add(2*time.Minute)); why != "" {
		t.Fatalf("completed early answer falsely retired a healthy session: %s", why)
	}
}

func TestDisconnectedPendingRequestDoesNotExpireNextIdleSession(t *testing.T) {
	old := firstResponseWait
	firstResponseWait = 100 * time.Millisecond
	t.Cleanup(func() { firstResponseWait = old })
	cl := connect(t)
	_, opened := cl.ask(t, `{"cmd":"listen","state":"start","mode":"manual"}`)
	cl.f.mu.Lock()
	original := cl.f.sess
	cl.f.mu.Unlock()
	cl.speech(t, 25)
	cl.hush(t, 65)
	cl.end(t, opened.ID)
	cl.cloud.mu.Lock()
	conn := cl.cloud.conn
	cl.cloud.mu.Unlock()
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	waitResponseRecovery(t, cl, original)
	cl.f.mu.Lock()
	reconnected := cl.f.sess
	cl.f.mu.Unlock()
	time.Sleep(1200 * time.Millisecond)
	cl.f.mu.Lock()
	same := cl.f.sess == reconnected
	cl.f.mu.Unlock()
	if !same || cl.f.Status().State != StateConnected {
		t.Fatal("the disconnected request's old deadline expired the new idle session")
	}
}
