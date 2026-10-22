package xiaozhi

// Dictation: one listen whose transcript is typed into the browser keyboard instead of answered.
//
// The cloud's recognizer is the one every turn already uses, and it hears Vietnamese and English
// without being told which. What is different is the end: the transcript is handed back as text,
// an abort follows it so the cloud stops working on an answer, and anything it says or calls
// anyway is dropped until somebody next asks Xiaozhi something. A dictation turn shows no voice
// screen, so the browser that asked for it keeps the display.

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/HuskerMinion/techo5/echod/internal/feature/wakeword"
	"github.com/HuskerMinion/techo5/echod/internal/hardware/speaker"
	"github.com/HuskerMinion/techo5/echod/internal/lib/safe"
)

// AbortDictation is the reason sent after a dictated transcript. Free text the server logs.
const AbortDictation = "dictation"

var (
	// ErrDictationBusy is Xiaozhi listening to, thinking about or saying something else.
	ErrDictationBusy = errors.New("xiaozhi is busy with another request")

	// ErrNothingHeard is a dictation that ended without words.
	ErrNothingHeard = errors.New("nothing was heard")

	// dictationMax caps one dictation, however long the room goes on talking.
	dictationMax = 30 * time.Second

	// dictationGrace is how long recognition may take after the listen closes.
	dictationGrace = 6 * time.Second

	// dictationChime sounds the start of listening and returns how long to let it fade, so the
	// tone is not sent up as the first word. A variable so tests stay off the speaker.
	dictationChime = func() time.Duration {
		if !wakeword.Tones(0, false) {
			return 0
		}
		wakeword.Chime(0, false)
		return wakeword.ChimeLength(0, false) + speaker.HardwareTail
	}
)

// Dictate listens once and returns what was said. finish closing ends the listen early, as a
// person tapping "done" does; recognition of what was already sent is still waited for. ctx ending
// abandons the dictation without waiting.
func (f *Feature) Dictate(ctx context.Context, finish <-chan struct{}) (string, error) {
	if ready, why := f.BackendReady(); !ready {
		return "", errors.New("xiaozhi cannot take a turn: " + why)
	}
	f.mu.Lock()
	thinking := f.response.turn != 0 && !f.response.done && !f.response.submitted.IsZero()
	f.mu.Unlock()
	down := f.downlink()
	if thinking || f.Listening() || followUpOpening.Load() != 0 || down != nil && down.isSpeaking() {
		return "", ErrDictationBusy
	}
	if wait := dictationChime(); wait > 0 {
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}

	heard := make(chan string, 1)
	t, err := f.openTurn(wakeMode(), 0, 0, false, heard)
	if err != nil {
		return "", err
	}
	defer func() {
		f.mu.Lock()
		if f.dictation == heard {
			f.dictation = nil
		}
		f.mu.Unlock()
	}()

	limit := time.NewTimer(dictationMax)
	defer limit.Stop()
	select {
	case text := <-heard:
		return dictated(text)
	case <-t.ended:
	case <-finish:
	case <-limit.C:
	case <-ctx.Done():
		f.endDictationTurn(t)
		return "", ctx.Err()
	}
	f.endDictationTurn(t)

	grace := time.NewTimer(dictationGrace)
	defer grace.Stop()
	select {
	case text := <-heard:
		return dictated(text)
	case <-grace.C:
		return "", ErrNothingHeard
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func dictated(text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", ErrNothingHeard
	}
	return text, nil
}

// endDictationTurn closes the dictation's own listen, which sends its stop, without marking an
// explicit cancellation: nothing queued by an earlier answer is being retired by a keyboard.
func (f *Feature) endDictationTurn(t *turn) {
	t.cancel()
	<-t.ended
}

// quietEvent takes what the cloud sends while dictation has quieted it: the transcript goes to the
// waiting dictation, and every recognition, reply and speech message after it is dropped. It runs
// on the socket's goroutine, so the abort goes out on a goroutine of its own.
func (f *Feature) quietEvent(e Event) bool {
	if e.Type != TypeSTT && e.Type != TypeLLM && e.Type != TypeTTS {
		return false
	}
	f.mu.Lock()
	if !f.quiet {
		f.mu.Unlock()
		return false
	}
	if e.Type != TypeSTT {
		f.mu.Unlock()
		return true
	}
	heard, sess, t := f.dictation, f.sess, f.turn
	f.dictation = nil
	if t != nil && t.dictation {
		if !t.manual {
			t.serverDone.Store(true)
		}
		t.cancel()
	}
	f.mu.Unlock()
	if heard != nil {
		heard <- e.Text
	}
	// The words are the keyboard's; the log keeps only their length.
	slog.Info("xiaozhi: dictation heard", "runes", utf8.RuneCountInString(e.Text), "delivered", heard != nil)
	if sess != nil {
		safe.Go("xiaozhi dictation abort", func() { _ = sess.Abort(AbortDictation) })
	}
	return true
}

// askingTurn reports a turn that asks Xiaozhi something, which a dictation does not.
func (f *Feature) askingTurn() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.turn != nil && !f.turn.dictation
}

// Dictating reports that the cloud's latest input was dictation, until somebody next asks Xiaozhi
// something. Screen actions requested in that interval answer words that were typed, not asked.
func Dictating() bool {
	f := Get()
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.quiet
}
