package xiaozhi

import (
	"sync"
	"testing"
	"time"
)

func TestScreenHandoffSkipsOnlyAutomaticFollowUp(t *testing.T) {
	cl := connect(t)
	SetAppHandoffPending(true)
	t.Cleanup(func() { SetAppHandoffPending(false) })
	if err := cl.f.FollowUp(time.Second); err == nil {
		t.Fatal("automatic capture opened over accepted screen action")
	}
	if cl.f.Listening() {
		t.Fatal("refused follow-up retained the microphone")
	}
	SetAppHandoffPending(false)
	if err := cl.f.FollowUp(time.Second); err != nil {
		t.Fatalf("normal follow-up setting was changed: %v", err)
	}
	cl.waitHeld(t, true, "normal follow-up still acquires microphone")
}

func TestScreenHandoffRacingAutomaticCaptureReleasesMicrophone(t *testing.T) {
	cl := connect(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	yielder.mu.Lock()
	old := yielder.fn
	yielder.mu.Unlock()
	YieldMic(func() { close(entered); <-release })
	t.Cleanup(func() { once.Do(func() { close(release) }); YieldMic(old); SetAppHandoffPending(false) })
	result := make(chan error, 1)
	go func() { result <- cl.f.FollowUp(8 * time.Second) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("capture did not reach handover")
	}
	SetAppHandoffPending(true)
	if !AnswerPlaying() {
		t.Fatal("display could launch during an unresolved automatic capture handover")
	}
	once.Do(func() { close(release) })
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("raced follow-up was accepted")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handoff deadlocked capture")
	}
	cl.waitHeld(t, false, "raced automatic capture gives microphone back")
	if cl.f.Listening() {
		t.Fatal("automatic capture outlived accepted screen action")
	}
	YieldMic(old)
	SetAppHandoffPending(false)
	if err := cl.f.Wake(); err != nil {
		t.Fatalf("later explicit wake failed: %v", err)
	}
	cl.waitHeld(t, true, "later explicit wake is independent of retired follow-up")
}

func TestDelayedAnswerCannotReopenCaptureAfterBrowserReady(t *testing.T) {
	cl := connect(t)
	oldEpoch := appHandoffEpoch.Load()
	SetAppHandoffPending(true)
	t.Cleanup(func() { SetAppHandoffPending(false) })
	// Simulate actual browser readiness clearing only the pending interval.
	SetAppHandoffPending(false)
	runAnswerFollowUp(oldEpoch, func() {
		if err := cl.f.FollowUp(time.Second); err != nil {
			t.Fatal(err)
		}
	})
	if cl.f.Listening() {
		t.Fatal("retired answer callback reopened listening over the ready browser")
	}
	if err := cl.f.FollowUp(time.Second); err != nil {
		t.Fatalf("a later legitimate follow-up was disabled globally: %v", err)
	}
	cl.waitHeld(t, true, "later conversation can still listen")
}

func TestCancellationGenerationDistinguishesDuplicateWakeFromReplacement(t *testing.T) {
	cl := connect(t)
	before := CancellationGeneration()
	if err := cl.f.Wake(); err != nil {
		t.Fatal(err)
	}
	after := CancellationGeneration()
	if after == before {
		t.Fatal("explicit new wake did not retire previous screen actions")
	}
	if err := cl.f.Wake(); err == nil {
		t.Fatal("duplicate wake was accepted")
	}
	if CancellationGeneration() != after {
		t.Fatal("wake inside same sentence cancelled accepted action")
	}
	cl.f.Cancel()
	if CancellationGeneration() == after {
		t.Fatal("explicit cancel left stale screen action live")
	}
}
