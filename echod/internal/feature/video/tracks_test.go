//go:build !dot

package video

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// A YouTube auto-caption file: a header, cue settings, inline timing and class tags, and rolling cues
// that repeat the line before.
const youtubeVTT = "\uFEFFWEBVTT\nKind: captions\nLanguage: vi\n\n" +
	"00:00:01.000 --> 00:00:03.500 align:start position:0%\n" +
	"xin<00:00:01.500><c> chào</c><00:00:02.000><c> các</c><c> bạn</c>\n\n" +
	"00:00:03.500 --> 00:00:03.510 align:start position:0%\n" +
	"xin chào các bạn\n\n" +
	"00:00:03.510 --> 00:00:06.000\n" +
	"xin chào các bạn\nhôm nay &amp; ngày mai\n\n" +
	"01:02:03.250 --> 01:02:04.000\n" +
	"Hết\n"

func TestParseVTTKeepsTimesAndPlainText(t *testing.T) {
	cues, err := parseVTT(strings.NewReader(youtubeVTT))
	if err != nil {
		t.Fatal(err)
	}
	if len(cues) != 4 {
		t.Fatalf("cues = %+v", cues)
	}
	if cues[0].start != time.Second || cues[0].end != 3500*time.Millisecond || cues[0].text != "xin chào các bạn" {
		t.Fatalf("first cue = %+v", cues[0])
	}
	if cues[2].text != "xin chào các bạn hôm nay & ngày mai" {
		t.Fatalf("joined lines = %q", cues[2].text)
	}
	if cues[3].start != time.Hour+2*time.Minute+3250*time.Millisecond {
		t.Fatalf("hours = %v", cues[3].start)
	}
	for at, want := range map[time.Duration]string{
		500 * time.Millisecond: "",
		2 * time.Second:        "xin chào các bạn",
		5 * time.Second:        "xin chào các bạn hôm nay & ngày mai",
		10 * time.Second:       "",
		time.Hour + 2*time.Minute + 3500*time.Millisecond: "Hết",
	} {
		if got := cueAt(cues, at); got != want {
			t.Errorf("cueAt(%v) = %q, want %q", at, got, want)
		}
	}
}

func TestParseVTTRejectsWhatIsNotSubtitles(t *testing.T) {
	for _, in := range []string{"", "<html>not found</html>", "WEBVTT\n\n00:00:05.000 --> 00:00:01.000\nbackwards\n",
		"WEBVTT\n\n00:00:01.000 -->\nno end\n"} {
		if _, err := parseVTT(strings.NewReader(in)); err == nil {
			t.Errorf("accepted %q", in)
		}
	}
}

func TestCheckTracksBoundsLabelsAndAddresses(t *testing.T) {
	ok := []Track{{Label: " Tiếng Việt ", URL: "https://www.youtube.com/api/timedtext?v=x&lang=vi"}}
	got, err := checkTracks(ok)
	if err != nil || got[0].Label != "Tiếng Việt" {
		t.Fatalf("checkTracks = %+v, %v", got, err)
	}
	for _, bad := range [][]Track{
		{{Label: "", URL: "https://example.com/a.vtt"}},
		{{Label: strings.Repeat("x", 61), URL: "https://example.com/a.vtt"}},
		{{Label: "x", URL: "file:///etc/passwd"}},
		{{Label: "x", URL: "http://127.0.0.1/a.vtt"}},
		make([]Track, maxTracks+1),
	} {
		if _, err := checkTracks(bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func TestCheckSubtitlesTakesBoundedTextNotAddresses(t *testing.T) {
	if got, err := checkSubtitles([]Track{{Label: "Tiếng Việt", Text: youtubeVTT}}); err != nil || got[0].Text != youtubeVTT {
		t.Fatalf("checkSubtitles = %+v, %v", got, err)
	}
	for _, bad := range [][]Track{
		{{Label: "x", URL: "https://example.com/a.vtt"}},
		{{Label: "x", URL: "https://example.com/a.vtt", Text: youtubeVTT}},
		{{Label: "x"}},
		{{Label: "x", Text: strings.Repeat("a", subtitleLimit+1)}},
		{{Label: "", Text: youtubeVTT}},
	} {
		if _, err := checkSubtitles(bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

// Switching the sound reloads where the video is with the other sound, keeps the video's identity,
// and is told in State; a sound it does not have is refused without touching the decoder.
func TestSwitchAudioReloadsWithTheOtherSound(t *testing.T) {
	f := fake(t, 100000, true)
	tracks := []Track{{Label: "Gốc", URL: "http://192.168.1.20/orig"}, {Label: "Tiếng Việt", URL: "http://192.168.1.20/vi"}}
	id, err := f.Play(Request{URL: "http://192.168.1.20/a.mp4", AudioURL: tracks[0].URL, AudioTracks: tracks, Origin: FromLocalApp})
	if err != nil {
		t.Fatal(err)
	}
	until(t, f, Playing)
	if st := f.State(); st.Audio != 0 || len(st.AudioTracks) != 2 || st.AudioTracks[1] != "Tiếng Việt" || st.Subtitle != -1 {
		t.Fatalf("state before = %+v", st)
	}
	old := f.cur
	if err := f.SwitchAudioID(id, 1); err != nil {
		t.Fatal(err)
	}
	if old.ctx.Err() == nil || f.cur == old {
		t.Fatal("the decoder was not reloaded")
	}
	if f.cur.req.AudioURL != tracks[1].URL || f.cur.audioValidated {
		t.Fatalf("reloaded with %q validated=%v", f.cur.req.AudioURL, f.cur.audioValidated)
	}
	st := until(t, f, Playing)
	if st.ID != id || st.Audio != 1 {
		t.Fatalf("state after = %+v", st)
	}
	// A seek afterwards keeps the chosen sound.
	if err := f.SeekID(id, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if f.cur.req.AudioURL != tracks[1].URL || f.State().Audio != 1 {
		t.Fatal("a seek went back to the first sound")
	}
	cur := f.cur
	if err := f.SwitchAudioID(id, 5); err == nil || f.cur != cur {
		t.Fatalf("an unknown sound = %v, decoder replaced %v", err, f.cur != cur)
	}
	if err := f.SwitchAudioID(id+1, 0); !errors.Is(err, ErrNoVideo) {
		t.Fatalf("another video's switch = %v", err)
	}
	f.StopID(id)
	f.runs.Wait()
}

// Subtitles chosen show their line due; they belong to the video chosen on, and none hides them.
func TestSubtitlesFollowTheVideoTheyWereChosenOn(t *testing.T) {
	f := fake(t, 100000, true)
	subs := []Track{{Label: "Tiếng Việt", Text: youtubeVTT}}
	id, err := f.Play(Request{URL: "http://192.168.1.20/a.mp4", Subtitles: subs, Origin: FromLocalApp})
	if err != nil {
		t.Fatal(err)
	}
	until(t, f, Playing)
	cues, err := parseVTT(strings.NewReader(youtubeVTT))
	if err != nil {
		t.Fatal(err)
	}
	f.SetSubtitleID(id, 0)
	if st := f.State(); st.Subtitle != 0 || st.Subtitles[0] != "Tiếng Việt" {
		t.Fatalf("state = %+v", st)
	}
	if err := f.SeekID(id, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if st := until(t, f, Playing); !strings.HasPrefix(st.Caption, "xin chào") {
		t.Fatalf("caption at %v = %q", st.Pos, st.Caption)
	}
	f.SetSubtitleID(id, -1)
	if st := f.State(); st.Subtitle != -1 || st.Caption != "" {
		t.Fatalf("after none = %+v", st)
	}
	f.SetSubtitleID(id, 3) // not one it has: nothing changes
	if f.State().Subtitle != -1 {
		t.Fatal("an unknown subtitle was chosen")
	}
	second, err := f.Play(Request{URL: "http://192.168.1.21/b.mp4"})
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.subs = subtitles{id: id, index: 0, cues: cues}
	f.mu.Unlock()
	if st := until(t, f, Playing); st.ID != second || st.Subtitle != -1 || st.Caption != "" {
		t.Fatalf("the next video showed the last one's subtitles: %+v", st)
	}
	f.StopID(second)
	f.runs.Wait()
}
