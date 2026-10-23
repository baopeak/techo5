//go:build !dot && !spot

package display

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"sync"
	"syscall"

	"github.com/HuskerMinion/techo5/echod/internal/feature/xiaozhi"
)

// The keyboard's Nói button reaches Xiaozhi's recognizer over one socket per browser lease, fd5 in
// the session. Only the keyboard inherits it, never the browser. It carries "listen" and "stop"
// lines up and one JSON reply per listen down: the transcript, or an error code the keyboard
// words for the person. The app root itself still has no microphone device.

type dictationReply struct {
	Text  string `json:"text,omitempty"`
	Error string `json:"error,omitempty"`
}

// dictateFunc is xiaozhi's Dictate, a parameter so the protocol is tested without a cloud.
type dictateFunc func(ctx context.Context, finish <-chan struct{}) (string, error)

// newDictationPair returns the daemon's end, non-blocking so closing it ends a pending read, and
// the end the launcher inherits.
func newDictationPair() (*os.File, *os.File, error) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	if err := syscall.SetNonblock(fds[0], true); err != nil {
		_ = syscall.Close(fds[0])
		_ = syscall.Close(fds[1])
		return nil, nil, err
	}
	return os.NewFile(uintptr(fds[0]), "browser dictation"), os.NewFile(uintptr(fds[1]), "browser dictation peer"), nil
}

func dictationError(err error) string {
	switch {
	case errors.Is(err, xiaozhi.ErrDictationBusy):
		return "busy"
	case errors.Is(err, xiaozhi.ErrNothingHeard):
		return "nothing"
	default:
		return "unavailable"
	}
}

// serveDictation answers one keyboard until its socket ends; the owner closes conn. One listen runs
// at a time, and a malformed or oversized line ends the conversation.
func serveDictation(conn io.ReadWriter, dictate dictateFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	var writes sync.Mutex
	send := func(r dictationReply) {
		data, _ := json.Marshal(r)
		writes.Lock()
		_, _ = conn.Write(append(data, '\n'))
		writes.Unlock()
	}
	var finish, done chan struct{}
	defer func() {
		cancel()
		if done != nil {
			<-done
		}
	}()
	lines := bufio.NewReaderSize(conn, 16)
	for {
		line, err := lines.ReadSlice('\n')
		if err != nil {
			return
		}
		if done != nil {
			select {
			case <-done:
				finish, done = nil, nil
			default:
			}
		}
		switch string(line) {
		case "listen\n":
			if done != nil {
				send(dictationReply{Error: "busy"})
				continue
			}
			finish, done = make(chan struct{}), make(chan struct{})
			go func(finish <-chan struct{}, done chan struct{}) {
				text, err := dictate(ctx, finish)
				// Before the reply, so a keyboard answered can listen again at once.
				close(done)
				if err != nil {
					if ctx.Err() == nil {
						slog.Info("browser dictation ended without text", "err", err)
						send(dictationReply{Error: dictationError(err)})
					}
					return
				}
				send(dictationReply{Text: text})
			}(finish, done)
		case "stop\n":
			if finish != nil {
				close(finish)
				finish = nil
			}
		default:
			return
		}
	}
}
