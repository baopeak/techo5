//go:build !dot

package video

import (
	"bufio"
	"errors"
	"io"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/unicode/norm"
)

// A video's other sounds and its subtitles (Request.AudioTracks, Request.Subtitles): a YouTube video
// handed over by the local browser can carry its dubs and its captions. The viewer switches the sound
// on the video's controls, which reloads the decoder where the video is with the other sound, as a
// seek does. Subtitles come as WebVTT text with the request, fetched by the browser with its cookies
// (YouTube refuses their addresses alone); the line due is State.Caption.

// maxTracks is the most sounds or subtitles one video may offer.
const maxTracks = 8

// subtitleLimit bounds one subtitle text, and subtitleCues the lines kept of one.
const (
	subtitleLimit = 512 << 10
	subtitleCues  = 20000
)

// cue is one subtitle line: shown from start until end.
type cue struct {
	start, end time.Duration
	text       string
}

// subtitles are what is shown of a video's subtitles: for which video, which of them, and their
// lines. Under Feature.mu.
type subtitles struct {
	id    uint64
	index int // -1: none
	cues  []cue
}

// checkTracks keeps the sounds a request may carry: at most maxTracks, each with a label and an
// address the player may fetch that is not the device itself.
func checkTracks(tracks []Track) ([]Track, error) {
	if len(tracks) > maxTracks {
		return nil, errors.New("too many tracks")
	}
	out := make([]Track, 0, len(tracks))
	for _, t := range tracks {
		u, err := CheckURL(t.URL)
		if err != nil {
			return nil, err
		}
		if ownHost(u.Hostname()) {
			return nil, errors.New("the device does not fetch from itself")
		}
		label, err := trackLabel(t.Label)
		if err != nil {
			return nil, err
		}
		out = append(out, Track{Label: label, URL: u.String()})
	}
	return out, nil
}

// checkSubtitles keeps the subtitles a request may carry: at most maxTracks, each a label and a
// bounded WebVTT text, never an address.
func checkSubtitles(tracks []Track) ([]Track, error) {
	if len(tracks) > maxTracks {
		return nil, errors.New("too many subtitles")
	}
	out := make([]Track, 0, len(tracks))
	for _, t := range tracks {
		label, err := trackLabel(t.Label)
		if err != nil {
			return nil, err
		}
		if t.URL != "" || t.Text == "" || len(t.Text) > subtitleLimit {
			return nil, errors.New("subtitles must be a bounded WebVTT text")
		}
		out = append(out, Track{Label: label, Text: t.Text})
	}
	return out, nil
}

func trackLabel(raw string) (string, error) {
	label := strings.TrimSpace(raw)
	if label == "" || len([]rune(label)) > 60 {
		return "", errors.New("a track needs a short label")
	}
	return label, nil
}

func labels(tracks []Track) []string {
	if len(tracks) == 0 {
		return nil
	}
	out := make([]string, len(tracks))
	for i, t := range tracks {
		out[i] = t.Label
	}
	return out
}

// SwitchAudioID plays video id on with its sound index, from where it is: the decoder is reloaded
// with that sound, keeping the video's identity, as a seek is.
func (f *Feature) SwitchAudioID(id uint64, index int) error {
	return f.reload(id, 0, true, func(s *session) error {
		if index < 0 || index >= len(s.req.AudioTracks) {
			return errors.New("video: no such sound")
		}
		s.req.AudioURL, s.audioIndex = s.req.AudioTracks[index].URL, index
		// The other sound has not been decoded yet: what the old one proved does not carry over.
		s.audioValidated = false
		return nil
	})
}

// SetSubtitleID shows video id's subtitles index, or none for -1. Subtitles that cannot be read show
// no line.
func (f *Feature) SetSubtitleID(id uint64, index int) {
	f.mu.Lock()
	s := f.cur
	if s == nil || s.id != id || index < -1 || index >= len(s.req.Subtitles) {
		f.mu.Unlock()
		return
	}
	f.subs = subtitles{id: id, index: index}
	text := ""
	if index >= 0 {
		text = s.req.Subtitles[index].Text
	}
	f.mu.Unlock()
	if text != "" {
		cues, err := parseVTT(strings.NewReader(text))
		if err != nil {
			slog.Warn("video: subtitles could not be read", "id", id, "err", err)
		}
		f.mu.Lock()
		if f.subs.id == id && f.subs.index == index {
			f.subs.cues = cues
		}
		f.mu.Unlock()
	}
	f.Changed.Emit(struct{}{})
}

// parseVTT reads WebVTT cues: their times and text, without styling tags, joined on one line each.
func parseVTT(r io.Reader) ([]cue, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 64<<10)
	var out []cue
	var cur *cue
	var lines []string
	flush := func() {
		if cur != nil && len(lines) > 0 && len(out) < subtitleCues {
			cur.text = norm.NFC.String(strings.Join(lines, " "))
			out = append(out, *cur)
		}
		cur, lines = nil, nil
	}
	for sc.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(sc.Text(), "\uFEFF"))
		switch {
		case line == "":
			flush()
		case strings.Contains(line, "-->"):
			flush()
			parts := strings.SplitN(line, "-->", 2)
			after := strings.Fields(parts[1]) // the end, then any cue settings
			if len(after) == 0 {
				continue
			}
			start, ok1 := vttTime(parts[0])
			end, ok2 := vttTime(after[0])
			if ok1 && ok2 && end > start {
				cur = &cue{start: start, end: end}
			}
		case cur != nil:
			if text := strings.TrimSpace(stripTags(line)); text != "" {
				// YouTube's rolling captions repeat the line before; keep each line once.
				if len(lines) == 0 || lines[len(lines)-1] != text {
					lines = append(lines, text)
				}
			}
		}
	}
	flush()
	if err := sc.Err(); err != nil {
		return nil, errors.New("subtitles could not be read")
	}
	if len(out) == 0 {
		return nil, errors.New("subtitles had no lines")
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].start < out[j].start })
	return out, nil
}

// vttTime reads 01:02:03.456 or 02:03.456.
func vttTime(s string) (time.Duration, bool) {
	s = strings.TrimSpace(s)
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	secs, err := strconv.ParseFloat(strings.Replace(parts[len(parts)-1], ",", ".", 1), 64)
	if err != nil || secs < 0 || secs >= 60 {
		return 0, false
	}
	total := secs
	mul := 60.0
	for i := len(parts) - 2; i >= 0; i-- {
		n, err := strconv.Atoi(parts[i])
		if err != nil || n < 0 {
			return 0, false
		}
		total += float64(n) * mul
		mul *= 60
	}
	return time.Duration(total * float64(time.Second)), true
}

// stripTags drops WebVTT's <c>, <00:00:01.000> and <i>-style tags, and decodes the few entities.
func stripTags(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch {
		case r == '<':
			depth++
		case r == '>' && depth > 0:
			depth--
		case depth == 0:
			b.WriteRune(r)
		}
	}
	return strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&nbsp;", " ", "&quot;", "\"", "&#39;", "'").Replace(b.String())
}

// cueAt is the line due at pos, "" between lines. YouTube's rolling captions overlap; the latest
// started wins.
func cueAt(cues []cue, pos time.Duration) string {
	i := sort.Search(len(cues), func(i int) bool { return cues[i].start > pos })
	for j := i - 1; j >= 0 && j >= i-8; j-- {
		if pos < cues[j].end {
			return cues[j].text
		}
	}
	return ""
}
