//go:build !dot && !spot

package display

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/feature/xiaozhi"
)

// keyboardSocket serves one fake keyboard and returns its end, as the session's fd5 would be.
func keyboardSocket(t *testing.T, dictate dictateFunc) (*os.File, *bufio.Reader, <-chan struct{}) {
	t.Helper()
	return keyboardSocketHolding(t, dictate, func(bool) {})
}

func keyboardSocketHolding(t *testing.T, dictate dictateFunc, hold func(bool)) (*os.File, *bufio.Reader, <-chan struct{}) {
	t.Helper()
	daemon, keyboard, err := newDictationPair()
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan struct{})
	go func() { defer close(served); serveDictation(daemon, dictate, hold) }()
	t.Cleanup(func() { keyboard.Close(); daemon.Close(); <-served })
	return keyboard, bufio.NewReader(keyboard), served
}

func readDictation(t *testing.T, r *bufio.Reader) dictationReply {
	t.Helper()
	line := make(chan []byte, 1)
	go func() { data, _ := r.ReadBytes('\n'); line <- data }()
	select {
	case data := <-line:
		var reply dictationReply
		if err := json.Unmarshal(data, &reply); err != nil {
			t.Fatalf("reply %q: %v", data, err)
		}
		return reply
	case <-time.After(5 * time.Second):
		t.Fatal("no dictation reply")
	}
	return dictationReply{}
}

func TestDictationSocketTypesWhatWasHeard(t *testing.T) {
	keyboard, replies, _ := keyboardSocket(t, func(context.Context, <-chan struct{}) (string, error) {
		return "tìm phở Hà Nội", nil
	})
	if _, err := keyboard.Write([]byte("listen\n")); err != nil {
		t.Fatal(err)
	}
	if got := readDictation(t, replies); got.Text != "tìm phở Hà Nội" || got.Error != "" {
		t.Fatalf("reply %+v", got)
	}
}

func TestDictationSocketStopFinishesAndRefusesASecondListen(t *testing.T) {
	keyboard, replies, _ := keyboardSocket(t, func(_ context.Context, finish <-chan struct{}) (string, error) {
		<-finish
		return "done", nil
	})
	keyboard.Write([]byte("listen\n"))
	keyboard.Write([]byte("listen\n"))
	if got := readDictation(t, replies); got.Error != "busy" {
		t.Fatalf("second listen answered %+v", got)
	}
	keyboard.Write([]byte("stop\n"))
	if got := readDictation(t, replies); got.Text != "done" {
		t.Fatalf("stop answered %+v", got)
	}
	keyboard.Write([]byte("listen\n"))
	keyboard.Write([]byte("stop\n"))
	if got := readDictation(t, replies); got.Text != "done" {
		t.Fatalf("a listen after the first one finished answered %+v", got)
	}
}

func TestDictationSocketWordsErrorsForTheKeyboard(t *testing.T) {
	for err, want := range map[error]string{
		xiaozhi.ErrNothingHeard:  "nothing",
		xiaozhi.ErrDictationBusy: "busy",
		os.ErrDeadlineExceeded:   "unavailable",
	} {
		keyboard, replies, _ := keyboardSocket(t, func(context.Context, <-chan struct{}) (string, error) { return "", err })
		keyboard.Write([]byte("listen\n"))
		if got := readDictation(t, replies); got.Error != want || got.Text != "" {
			t.Fatalf("%v answered %+v, want %s", err, got, want)
		}
	}
}

// The keyboard's full screen holds the wake words once however often it says so, lets them go when it
// says windowed, and lets them go when its socket ends while it still held them.
func TestKeyboardFullScreenHoldsTheWakeWordsUntilItEnds(t *testing.T) {
	holds := make(chan bool, 8)
	keyboard, _, served := keyboardSocketHolding(t, func(context.Context, <-chan struct{}) (string, error) { return "", nil },
		func(on bool) { holds <- on })
	next := func(want bool) {
		t.Helper()
		select {
		case got := <-holds:
			if got != want {
				t.Fatalf("hold %v, want %v", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no hold %v", want)
		}
	}
	keyboard.Write([]byte("fullscreen\nfullscreen\n"))
	next(true)
	keyboard.Write([]byte("windowed\n"))
	next(false)
	keyboard.Write([]byte("fullscreen\n"))
	next(true)
	keyboard.Close()
	next(false)
	<-served
	select {
	case on := <-holds:
		t.Fatalf("an extra hold %v", on)
	default:
	}
}

func TestDictationSocketEndsWithTheSessionAndAMalformedLine(t *testing.T) {
	canceled := make(chan struct{})
	keyboard, _, served := keyboardSocket(t, func(ctx context.Context, _ <-chan struct{}) (string, error) {
		<-ctx.Done()
		close(canceled)
		return "", ctx.Err()
	})
	keyboard.Write([]byte("listen\n"))
	keyboard.Write([]byte("speak louder please\n"))
	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Fatal("an oversized line did not end the keyboard's socket")
	}
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("the dictation in progress outlived its socket")
	}
}
