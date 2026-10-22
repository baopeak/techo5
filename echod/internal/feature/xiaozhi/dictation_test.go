package xiaozhi

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

type dictationResult struct {
	text string
	err  error
}

func dictate(t *testing.T, cl *client, finish <-chan struct{}) <-chan dictationResult {
	t.Helper()
	old := dictationChime
	dictationChime = func() time.Duration { return 0 }
	t.Cleanup(func() { dictationChime = old })
	out := make(chan dictationResult, 1)
	go func() {
		text, err := cl.f.Dictate(context.Background(), finish)
		out <- dictationResult{text, err}
	}()
	cl.waitHeld(t, true, "dictation opened the microphone")
	return out
}

func (c *client) transcript(t *testing.T, text string) {
	t.Helper()
	c.cloud.mu.Lock()
	conn := c.cloud.conn
	c.cloud.mu.Unlock()
	if err := conn.WriteJSON(Event{Type: TypeSTT, Text: text}); err != nil {
		t.Fatalf("sending transcript: %v", err)
	}
}

func dictationAborts(c *cloud) int {
	var n int
	for _, raw := range c.raw() {
		var m struct{ Type, Reason string }
		if json.Unmarshal(raw, &m) == nil && m.Type == TypeAbort && m.Reason == AbortDictation {
			n++
		}
	}
	return n
}

func TestDictationTypesTheTranscriptInsteadOfAnsweringIt(t *testing.T) {
	cl := connect(t)
	var mu sync.Mutex
	var screens []State
	stopWatching := Changed.Listen(func(s State) { mu.Lock(); screens = append(screens, s); mu.Unlock() })
	defer stopWatching()

	result := dictate(t, cl, nil)
	if cl.f.askingTurn() {
		t.Fatal("dictation counts as a voice request, which would close the browser asking for it")
	}
	cl.speech(t, 10)
	cl.transcript(t, " đà lạt mùa hoa dã quỳ ")
	select {
	case r := <-result:
		if r.err != nil || r.text != "đà lạt mùa hoa dã quỳ" {
			t.Fatalf("dictation returned %q, %v", r.text, r.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the transcript never reached the dictation")
	}
	deadline := time.Now().Add(5 * time.Second)
	for dictationAborts(cl.cloud) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the cloud was not told to drop its answer; it was sent %v", cl.spoken(t))
		}
		time.Sleep(5 * time.Millisecond)
	}
	cl.waitHeld(t, false, "the dictation gave the microphone back")

	// An answer the cloud sends anyway is not played, and its words reach no screen.
	cl.cloud.speak(t, answer(t, defaultDownlinkRate, 60, 8, 440))
	time.Sleep(300 * time.Millisecond)
	if cl.speaking() {
		t.Fatal("the answer to dictated words was played")
	}
	mu.Lock()
	for _, s := range screens {
		if s.Phase != "idle" || s.Heard != "" {
			t.Fatalf("dictation drove the voice screen: %+v", s)
		}
	}
	mu.Unlock()
	cl.f.mu.Lock()
	quiet := cl.f.quiet
	cl.f.mu.Unlock()
	if !quiet {
		t.Fatal("screen actions answering the dictated words would be accepted")
	}

	// The next spoken request is an ordinary one again.
	if err := cl.f.Wake(); err != nil {
		t.Fatalf("wake after dictation: %v", err)
	}
	cl.f.mu.Lock()
	quiet = cl.f.quiet
	cl.f.mu.Unlock()
	if quiet || !cl.f.askingTurn() {
		t.Fatal("a wake word after dictation is still treated as dictation")
	}
}

func TestDictationFinishedEarlyWaitsForRecognition(t *testing.T) {
	cl := connect(t)
	finish := make(chan struct{})
	result := dictate(t, cl, finish)
	cl.speech(t, 10)
	close(finish)
	cl.cloud.waitStop(t)
	cl.waitHeld(t, false, "finishing gave the microphone back")
	cl.transcript(t, "weather in Hanoi")
	select {
	case r := <-result:
		if r.err != nil || r.text != "weather in Hanoi" {
			t.Fatalf("dictation returned %q, %v", r.text, r.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("recognition after an early finish was not waited for")
	}
}

func TestDictationWithoutWordsSaysSo(t *testing.T) {
	old := dictationGrace
	dictationGrace = 200 * time.Millisecond
	t.Cleanup(func() { dictationGrace = old })
	cl := connect(t)
	finish := make(chan struct{})
	result := dictate(t, cl, finish)
	close(finish)
	select {
	case r := <-result:
		if !errors.Is(r.err, ErrNothingHeard) {
			t.Fatalf("silent dictation returned %q, %v", r.text, r.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("silent dictation never ended")
	}
}

func TestDictationDoesNotTakeAnOpenTurn(t *testing.T) {
	cl := connect(t)
	cl.ask(t, `{"cmd":"listen","state":"start","mode":"manual"}`)
	if _, err := cl.f.Dictate(context.Background(), nil); !errors.Is(err, ErrDictationBusy) {
		t.Fatalf("dictation over an open request returned %v", err)
	}
}
