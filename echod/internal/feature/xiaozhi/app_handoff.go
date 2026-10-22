package xiaozhi

import "sync/atomic"

// appHandoffPending suppresses only the automatic follow-up belonging to an
// accepted screen action. It never changes the user's conversation setting.
var appHandoffPending atomic.Bool
var appHandoffEpoch atomic.Uint64
var followUpOpening atomic.Int64

// SetAppHandoffPending is owned by the display's accepted app-intent lifecycle.
// A monotonically increasing epoch permanently retires older answer callbacks,
// even when browser readiness later clears the pending flag.
func SetAppHandoffPending(pending bool) {
	if pending {
		appHandoffEpoch.Add(1)
	}
	appHandoffPending.Store(pending)
}

// AnswerPlaying reports the real downlink tail, which can outlive the protocol's
// TTSStop/idle publication. A screen action must not steal that output interval.
func AnswerPlaying() bool {
	f := Get()
	if followUpOpening.Load() != 0 || f.Listening() {
		return true
	}
	down := f.downlink()
	return down != nil && down.isSpeaking()
}

// runAnswerFollowUp retires callbacks scheduled before a later screen intent.
// FollowUp itself performs the pending and in-flight capture checks.
func runAnswerFollowUp(epoch uint64, fn func()) {
	followUpOpening.Add(1)
	defer followUpOpening.Add(-1)
	if appHandoffPending.Load() || appHandoffEpoch.Load() != epoch {
		return
	}
	fn()
}
