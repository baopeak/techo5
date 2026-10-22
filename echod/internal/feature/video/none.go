//go:build dot

package video

import "time"

// Here is whether this device plays videos: the Dot does not (doc.go).
const Here = false

func Play(Request) (uint64, error)      { return 0, ErrNotHere }
func Stop()                             {}
func StopID(uint64)                     {}
func StopIDDone(uint64) <-chan struct{} { return stoppedDone }
func Seek(time.Duration) error          { return ErrNotHere }
func SeekBy(time.Duration) error        { return ErrNotHere }
func Pause()                            {}
func Resume()                           {}
func Current() State                    { return State{Phase: Idle} }
func SetOn(bool)                        {}
func SetDLNA(bool)                      {}
func Listen(func())                     {}
func DLNAOn() bool                      { return false }
func Installed() bool                   { return false }

var stoppedDone = func() <-chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}()
