package video

import (
	"errors"
	"time"
)

// Origin is who asked for a video.
type Origin int

const (
	// FromHomeAssistant is the play_video action: over the ESPHome link, which is already
	// authenticated, so it never asks on the screen.
	FromHomeAssistant Origin = iota

	// FromDLNA is a DLNA controller, which anybody on the network can be: the first video from an
	// address asks on the screen first.
	FromDLNA

	// FromLocalApp is a locally launched app handing playback to the native player.
	FromLocalApp
)

// Request is a video to play.
type Request struct {
	URL      string
	AudioURL string // optional separate sound input; otherwise URL supplies both streams
	Title    string

	// AudioTracks are the sounds the viewer may switch between, the first the one AudioURL plays;
	// Subtitles are WebVTT texts the viewer may show (tracks.go). Both are optional.
	AudioTracks []Track
	Subtitles   []Track

	Origin Origin
	// From is the address that asked, for a DLNA controller: what the screen names and remembers.
	From string
}

// Track is one sound or subtitle choice: what the screen names it, and a sound's address or the
// subtitles' WebVTT text.
type Track struct {
	Label string
	URL   string
	Text  string
}

// Phase is what the player is doing, as Home Assistant's Video sensor says it.
type Phase string

const (
	Idle    Phase = "idle"
	Asking  Phase = "asking"  // a DLNA video waiting for an answer on the screen
	Loading Phase = "loading" // finding out what it is, and the first frames
	Playing Phase = "playing"
	Paused  Phase = "paused"
)

// State is the player now.
type State struct {
	Phase Phase
	// ID belongs to the playback request, not its decoder: seeking retains it.
	ID     uint64
	Title  string
	Host   string
	Origin Origin
	From   string

	// LastID is the last playback request actually begun, retained across seeks and while idle;
	// pending DLNA asks do not change it.
	LastID uint64

	// Pos is the absolute video position (seek offset plus elapsed playback); Dur is its duration,
	// or zero when unknown. Only known-duration videos support seeking.
	Pos, Dur time.Duration

	// Frames is whether the picture has begun to arrive.
	Frames bool

	// AudioTracks and Subtitles name the video's choices; Audio is the sound playing, Subtitle the
	// subtitles shown (-1: none), and Caption their line due now.
	AudioTracks, Subtitles []string
	Audio, Subtitle        int
	Caption                string

	// AskID is the DLNA video whose question is on the screen, when there is one: while another video
	// plays, the state is that video's, and this is how the one asking knows it is still asked about.
	AskID uint64

	// Shown and Dropped count frames.
	Shown, Dropped int

	// Ended is the ID of the last video to play to its end by itself; Failed the ID of the last that
	// failed, and Err why, ErrAt when.
	Ended  uint64
	Failed uint64
	Err    string
	ErrAt  time.Time
}

// Active is whether a video is up, from asking through to paused.
func (s State) Active() bool { return s.Phase != Idle && s.Phase != "" }

var (
	// ErrOff is a video asked for with the Video switch off (or DLNA video, for a DLNA one).
	ErrOff = errors.New("video: videos are off on this device")
	// ErrNotHere is a device with no video player: the Dot.
	ErrNotHere = errors.New("video: this device has no video player")
	// ErrDeclined is an address the screen just said Not now to.
	ErrDeclined = errors.New("video: not now")
	// ErrBusy is a DLNA video from one address while the screen is asking about another's: the question
	// on the screen is not changed under the finger answering it.
	ErrBusy = errors.New("video: the screen is asking about another video")
	// ErrNotInstalled is an image without the decoder.
	ErrNotInstalled = errors.New("video: this image has no video decoder")
	// ErrNotSeekable is a stream with no known VOD duration.
	ErrNotSeekable = errors.New("video: seeking requires a known video duration")
	// ErrNoVideo is a seek whose video is no longer current.
	ErrNoVideo = errors.New("video: that video is no longer playing")
)

// DLNAProtocols are what the DLNA renderer adds to its sink protocols while DLNA video is on.
const DLNAProtocols = "http-get:*:video/mp4:*,http-get:*:video/x-matroska:*,http-get:*:video/mp2t:*," +
	"http-get:*:video/quicktime:*,http-get:*:application/vnd.apple.mpegurl:*,http-get:*:application/x-mpegURL:*"
