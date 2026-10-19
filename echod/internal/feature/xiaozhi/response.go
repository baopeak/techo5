package xiaozhi

import (
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/config"
)

// Recognition normally arrives shortly after listen/stop. Give a silent server
// a bounded grace period without applying the much shorter mid-audio stall
// deadline to ASR, model inference or a tool call.
var firstResponseWait = 15 * time.Second

// responseWait belongs to one installed turn and is guarded by Feature.mu.
// No audio or transcript is retained. A new turn replaces it; pings do not
// count as progress on the user's request.
type responseWait struct {
	turn                 int
	submitted, progress  time.Time
	heard, started, done bool
}

func (w *responseWait) submit(now time.Time) {
	if !w.done && w.submitted.IsZero() {
		w.submitted = now
	}
}

func (w *responseWait) observe(e Event, now time.Time) {
	switch {
	case len(e.Audio) > 0, e.Type == TypeTTS && e.State == TTSStop && w.started:
		// Audio now belongs to the existing downlink stall watch. An empty
		// completed answer is complete too, not another pending request.
		w.done = true
	case e.Type == TypeSTT && e.Text != "", e.Type == TypeLLM && e.Text != "", e.Type == TypeTTS && (e.State == TTSStart || e.State == TTSSentenceStart):
		w.heard, w.progress = true, now
	}
	if e.Type == TypeTTS && e.State == TTSStart {
		w.started = true
	}
}

func (w *responseWait) expired(now time.Time) string {
	if w.done || w.submitted.IsZero() {
		return ""
	}
	if !w.heard {
		if now.Sub(w.submitted) >= firstResponseWait {
			return "no recognition or answer after listen stop"
		}
		return ""
	}
	// Once the server has recognized the request, preserve the existing
	// model/tool thinking budget rather than cutting off a legitimate search.
	last := w.progress
	if last.Before(w.submitted) {
		last = w.submitted
	}
	if now.Sub(last) >= time.Duration(config.DefaultMaxThink)*time.Second {
		return "the recognized request stopped making progress"
	}
	return ""
}

// Reserve this expired session before closing it off the lock. An explicit wake
// racing the watchdog must reconnect, not install a new turn on a socket the
// watchdog has already decided to discard.
func (f *Feature) expireResponse(sess *Session, down *downlink, now time.Time) (string, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sess != sess || f.down != down || f.turn != nil {
		return "", 0
	}
	why := f.response.expired(now)
	if why == "" {
		return "", 0
	}
	turn := f.response.turn
	f.response = responseWait{}
	f.sess = nil
	return why, turn
}
