//go:build !dot && !spot

package display

import (
	"errors"
	"io"
	"os"
	"time"
)

type browserReadyResult struct {
	generation uint64
	err        error
}

func readBrowserReady(r io.Reader) error {
	var frame [6]byte
	if _, err := io.ReadFull(r, frame[:]); err != nil {
		return errors.New("browser readiness pipe closed before acknowledgement")
	}
	if string(frame[:]) != "ready\n" {
		return errors.New("invalid browser readiness acknowledgement")
	}
	var extra [1]byte
	if n, err := r.Read(extra[:]); n != 0 || err != io.EOF {
		return errors.New("browser readiness pipe must close after acknowledgement")
	}
	return nil
}

func (a *localApps) readReady(pipe *os.File, wake func()) {
	a.readyPipe = pipe
	a.browserReady = make(chan browserReadyResult, 1)
	ready, generation := a.browserReady, a.generation
	go func() {
		defer pipe.Close()
		// Session startup has sequential audio/X11/WM/keyboard/window bounds
		// totaling 65 seconds; leave setup margin without delaying cancellation.
		_ = pipe.SetReadDeadline(time.Now().Add(75 * time.Second))
		ready <- browserReadyResult{generation: generation, err: readBrowserReady(pipe)}
		wake()
	}()
}

func (a *localApps) clearReady() {
	if a.readyPipe != nil {
		_ = a.readyPipe.Close()
		a.readyPipe = nil
	}
	a.browserReady = nil
}
