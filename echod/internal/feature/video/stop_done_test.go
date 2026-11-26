//go:build !dot

package video

import (
	"sync"
	"testing"
	"time"
)

// delayedOwnedSession registers a real session.run whose decoder cannot finish
// until release. Start it only after cancellation, so no subprocess is needed.
func delayedOwnedSession(t *testing.T, f *Feature, id uint64) (*session, func(), func()) {
	t.Helper()
	u, err := CheckURL("http://192.168.1.20/a.mp4")
	if err != nil {
		t.Fatal(err)
	}
	s := newSession(id, Request{URL: u.String()}, u, f.scr, testW, testH)
	decoderDone := make(chan struct{})
	s.dec = &decoder{done: decoderDone}
	s.ended, s.changed = f.sessionEnded, func() {}
	f.mu.Lock()
	f.addRunLocked(id)
	f.mu.Unlock()
	finished := make(chan struct{})
	var started, released sync.Once
	start := func() {
		started.Do(func() {
			go func() {
				defer close(finished)
				f.runSession(s, false)
			}()
		})
	}
	release := func() { released.Do(func() { close(decoderDone) }) }
	t.Cleanup(func() {
		s.cancel()
		f.mu.Lock()
		if f.cur == s {
			f.cur = nil
		}
		f.mu.Unlock()
		release()
		start()
		awaitStopDone(t, finished)
	})
	return s, start, release
}

func requireStopPending(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
		t.Fatal("completion closed while owned teardown was still blocked")
	default:
	}
}

func awaitStopDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("owned teardown did not complete")
	}
}

func TestStopIDDoneWaitsForDecoderAndRunCallback(t *testing.T) {
	f := &Feature{}
	s, start, release := delayedOwnedSession(t, f, 1)
	callback := make(chan struct{})
	callbackRelease := make(chan struct{})
	var released sync.Once
	unblock := func() { released.Do(func() { close(callbackRelease) }) }
	t.Cleanup(unblock)
	s.ended = func(s *session, err error, itself bool) {
		f.sessionEnded(s, err, itself)
		close(callback)
		<-callbackRelease
	}
	f.cur = s
	done := f.StopIDDone(s.id)
	// stop() has returned and State is already idle, but run has not even started.
	if f.State().Active() || s.ctx.Err() == nil {
		t.Fatal("matching session was not stopped")
	}
	requireStopPending(t, done)
	start()
	requireStopPending(t, done)
	requireStopPending(t, f.StopIDDone(s.id))
	release()
	awaitStopDone(t, callback)
	// Decoder reap alone is insufficient: the end callback still owns run work.
	requireStopPending(t, done)
	unblock()
	awaitStopDone(t, done)
	awaitStopDone(t, f.StopIDDone(s.id))
	awaitStopDone(t, f.StopIDDone(0))
	awaitStopDone(t, f.StopIDDone(999))
}

func TestStopIDDoneIncludesRetiredSeekGeneration(t *testing.T) {
	f := fake(t, 100000, true)
	old, startOld, releaseOld := delayedOwnedSession(t, f, 1)
	old.info = Info{Width: testW, Height: testH, FPS: 30, Duration: time.Minute, AudioIndex: -1}
	old.rate = FrameRate(30)
	f.mu.Lock()
	f.cur, f.lastID, f.seq = old, old.id, old.id
	f.mu.Unlock()
	if err := f.SeekID(old.id, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	startOld() // seek canceled this generation; its decoder reap remains blocked.
	until(t, f, Playing)
	f.mu.Lock()
	current := f.cur
	f.mu.Unlock()
	if current == nil || current == old || current.id != old.id {
		t.Fatal("seek did not replace the decoder while preserving public ID")
	}
	done := f.StopIDDone(old.id)
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.mu.Lock()
		owned := f.owned[old.id]
		retiredOnly := owned != nil && owned.count == 1
		f.mu.Unlock()
		requireStopPending(t, done)
		if retiredOnly {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("current seek generation did not finish teardown")
		}
		time.Sleep(time.Millisecond)
	}
	requireStopPending(t, done)
	requireStopPending(t, f.StopIDDone(old.id))
	releaseOld()
	awaitStopDone(t, done)
}

func TestStopIDDoneStaleIDDoesNotStopOrWaitForNewVideo(t *testing.T) {
	f := &Feature{}
	old, startOld, releaseOld := delayedOwnedSession(t, f, 1)
	newer, startNew, releaseNew := delayedOwnedSession(t, f, 2)
	old.cancel()
	startOld()
	f.cur = newer
	done := f.StopIDDone(old.id)
	if f.cur != newer || newer.ctx.Err() != nil {
		t.Fatal("stale stop canceled the replacement video")
	}
	requireStopPending(t, done)
	releaseOld()
	awaitStopDone(t, done) // newer is still active and its run is still registered.
	if f.cur != newer || newer.ctx.Err() != nil {
		t.Fatal("old completion altered the replacement video")
	}
	newDone := f.StopIDDone(newer.id)
	startNew()
	requireStopPending(t, newDone)
	releaseNew()
	awaitStopDone(t, newDone)
}

func TestStopIDDoneQuestionDoesNotStopDifferentCurrentVideo(t *testing.T) {
	f := &Feature{}
	current, start, release := delayedOwnedSession(t, f, 2)
	f.cur, f.ask = current, &ask{id: 1}
	awaitStopDone(t, f.StopIDDone(1))
	if f.ask != nil || f.cur != current || current.ctx.Err() != nil {
		t.Fatal("stopping the question affected a different current video")
	}
	done := f.StopIDDone(current.id)
	start()
	release()
	awaitStopDone(t, done)
}
