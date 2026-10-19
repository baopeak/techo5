package xiaozhi

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/text/unicode/norm"

	"github.com/HuskerMinion/techo5/echod/internal/config"
	"github.com/HuskerMinion/techo5/echod/internal/hardware/speaker"
)

func TestConversationEndMatchesOnlyWholeClosingUtterances(t *testing.T) {
	for _, text := range []string{
		"vậy được rồi", "vậy là được rồi", "thôi được rồi",
		"vậy được rồi cảm ơn", "vậy được rồi cảm ơn bạn",
		"vậy được rồi cảm ơn đệ", "được rồi cảm ơn", "được rồi cảm ơn bạn", "được rồi cảm ơn đệ",
		"tạm biệt", "dừng trò chuyện", "kết thúc trò chuyện",
		"  VẬY ĐƯỢC RỒI!  ", "Vậy, được rồi…", "\tTẠM\nBIỆT.\u00a0",
		norm.NFD.String("Vậy được rồi, cảm ơn bạn!"),
		norm.NFD.String("Tạm biệt."),
	} {
		t.Run(text, func(t *testing.T) {
			if !isConversationEnd(text) {
				t.Errorf("explicit closing %q was not recognized", text)
			}
		})
	}
	for _, text := range []string{
		"", " ", "...", "cảm ơn", "cảm ơn bạn", "được rồi", "ừ", "ok",
		"vậy được rồi?", "Tạm biệt？", "vậy? được rồi",
		"nói câu vậy được rồi", "bạn vừa nói tạm biệt",
		"vậy được rồi nhưng kể thêm về Đà Lạt", "vậy được rồi cảm ơn bạn và cho tôi biết thời tiết",
		"tạm biệt rồi hãy bật đèn", "đừng kết thúc trò chuyện", "vậy được rồi nhé",
		"đà lạt có gì hay?", "cho tôi biết thời tiết Đà Lạt",
	} {
		t.Run(text, func(t *testing.T) {
			if isConversationEnd(text) {
				t.Errorf("ordinary or continued request %q ended the conversation", text)
			}
		})
	}
}

// conversationAnswered installs the same consumer behavior as voice: a completed
// answer requests a real follow-up turn. Results are buffered so the hook never
// blocks the speaker or a cleanup path.
func conversationAnswered(t *testing.T, cl *client) <-chan error {
	t.Helper()
	answered.mu.Lock()
	old := answered.fn
	answered.mu.Unlock()
	results := make(chan error, 16)
	OnAnswered(func() {
		err := cl.f.FollowUp(8 * time.Second)
		select {
		case results <- err:
		default:
		}
	})
	t.Cleanup(func() { OnAnswered(old) })
	return results
}

func conversationFollowedUp(t *testing.T, cl *client, results <-chan error) {
	t.Helper()
	select {
	case err := <-results:
		if err != nil {
			t.Fatalf("completed ordinary answer could not open follow-up: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("completed ordinary answer did not request follow-up")
	}
	cl.waitHeld(t, true, "the completed ordinary answer reopened the microphone")
	if !cl.f.Listening() {
		t.Fatal("follow-up owns the microphone but does not report Listening")
	}
}

// Send STT over the actual fake-cloud socket, and wait for its control event so
// subsequent playback cannot race ahead of the client's transcript handling.
func conversationTranscript(t *testing.T, cl *client, text string) {
	t.Helper()
	cl.cloud.mu.Lock()
	conn := cl.cloud.conn
	cl.cloud.mu.Unlock()
	if err := conn.WriteJSON(Event{Type: TypeSTT, Text: text}); err != nil {
		t.Fatalf("sending transcript: %v", err)
	}
	for {
		r, err := cl.term.next()
		if err != nil {
			t.Fatalf("waiting for transcript: %v", err)
		}
		if r.Event == eventMessage && r.Msg != nil && r.Msg.Type == TypeSTT && r.Msg.Text == text {
			return
		}
	}
}

// Decode a real Opus answer arriving over the socket and drive the card through
// its tail. Seeing PCM and the completed speech record rules out fixing closing
// behavior by muting or interrupting the goodbye.
func conversationAnswer(t *testing.T, cl *client) {
	t.Helper()
	const packets, frameMS = 8, 60
	down := cl.downlink(t)
	var card atomic.Uint64
	down.mu.Lock()
	card.Store(down.played)
	down.card = func() uint64 { return card.Load() }
	down.mu.Unlock()
	cl.cloud.speak(t, answer(t, defaultDownlinkRate, frameMS, packets, 440))
	waitDecoded(t, down, packets*speaker.Rate*frameMS/1000)
	for {
		r, err := cl.term.next()
		if err != nil {
			t.Fatalf("waiting for TTS stop: %v", err)
		}
		if r.Event == eventMessage && r.Msg != nil && r.Msg.Type == TypeTTS && r.Msg.State == TTSStop {
			break
		}
	}
	budget := packets*speaker.Rate*frameMS/1000 + int(cushionFrames+tailFrames) + speakerPeriod
	out := drive(t, down, &card, budget)
	if lo, hi := span(out); lo < 0 || hi <= lo {
		t.Fatal("the completed answer rendered no audible PCM")
	}
	r := waitSpeech(t, cl.term)
	if r.Speech == nil || r.Speech.Packets != packets || r.Speech.State != "the tail was heard" {
		t.Fatalf("answer did not play to completion: %+v", r.Speech)
	}
}

func TestClosingAnswerPlaysWithoutReopeningAndNewWakeResumesConversation(t *testing.T) {
	cl := connect(t)
	if err := config.Set().Microphone().DeviceEnds(false); err != nil {
		t.Fatal(err)
	}
	results := conversationAnswered(t, cl)
	if err := cl.f.FollowUp(8 * time.Second); err != nil {
		t.Fatal(err)
	}
	cl.waitHeld(t, true, "the existing conversation is listening")
	conversationTranscript(t, cl, "vậy được rồi")
	cl.waitHeld(t, false, "closing STT ended the existing follow-up")
	_, before := cl.holding()
	conversationAnswer(t, cl)
	if err := cl.f.FollowUp(8 * time.Second); err == nil {
		t.Fatal("a late follow-up after closing was accepted")
	}
	if held, takes := cl.holding(); held || takes != before || cl.f.Listening() {
		t.Fatalf("goodbye or late follow-up reopened the microphone: held=%v takes=%d, before=%d", held, takes, before)
	}
	select {
	case err := <-results:
		t.Fatalf("goodbye incorrectly requested automatic follow-up: %v", err)
	default:
	}

	if err := cl.f.Wake(); err != nil {
		t.Fatalf("a new explicit wake after closing was refused: %v", err)
	}
	cl.waitHeld(t, true, "the new wake starts a new conversation")
	conversationTranscript(t, cl, "Đà Lạt có gì hay?")
	cl.waitHeld(t, false, "the server ended the new question")
	conversationAnswer(t, cl)
	conversationFollowedUp(t, cl, results)
}

func TestOrdinaryAnswerStillOpensFollowUp(t *testing.T) {
	cl := connect(t)
	if err := config.Set().Microphone().DeviceEnds(false); err != nil {
		t.Fatal(err)
	}
	results := conversationAnswered(t, cl)
	if err := cl.f.Wake(); err != nil {
		t.Fatal(err)
	}
	cl.waitHeld(t, true, "the ordinary question owns the microphone")
	conversationTranscript(t, cl, "Kể thêm về Đà Lạt")
	cl.waitHeld(t, false, "ordinary STT ends the original capture")
	conversationAnswer(t, cl)
	conversationFollowedUp(t, cl, results)
}

func TestClosingManualFollowUpReleasesMicrophone(t *testing.T) {
	cl := connect(t)
	if err := config.Set().Microphone().DeviceEnds(true); err != nil {
		t.Fatal(err)
	}
	results := conversationAnswered(t, cl)
	if err := cl.f.FollowUp(8 * time.Second); err != nil {
		t.Fatal(err)
	}
	cl.waitHeld(t, true, "manual follow-up is listening")
	conversationTranscript(t, cl, "Được rồi, cảm ơn đệ.")
	cl.waitHeld(t, false, "explicit closing releases even a manual follow-up")
	cl.cloud.waitStop(t)
	if cl.f.Listening() {
		t.Fatal("manual follow-up remained Listening after closing")
	}
	if err := cl.f.FollowUp(8 * time.Second); err == nil {
		t.Fatal("manual closing allowed a late follow-up")
	}
	conversationAnswer(t, cl)
	select {
	case err := <-results:
		t.Fatalf("manual goodbye requested automatic follow-up: %v", err)
	default:
	}
	if cl.f.Listening() {
		t.Fatal("completed manual goodbye reopened Listening")
	}
	if err := cl.f.Wake(); err != nil {
		t.Fatalf("explicit wake after manual closing failed: %v", err)
	}
	cl.waitHeld(t, true, "new wake after manual closing owns the microphone")
	cl.speech(t, 25)
	cl.hush(t, 65)
	cl.waitHeld(t, false, "the new manual question ended after speech and quiet")
	conversationTranscript(t, cl, "Kể thêm về Đà Lạt")
	conversationAnswer(t, cl)
	conversationFollowedUp(t, cl, results)
}

func TestClosingWhileFollowUpOpensReleasesMicAndPreservesNextWake(t *testing.T) {
	cl := connect(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	yielder.mu.Lock()
	old := yielder.fn
	yielder.mu.Unlock()
	YieldMic(func() {
		close(entered)
		<-release
	})
	t.Cleanup(func() {
		once.Do(func() { close(release) })
		YieldMic(old)
	})
	opened := make(chan error, 1)
	go func() { opened <- cl.f.FollowUp(8 * time.Second) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("follow-up never entered microphone handover")
	}
	conversationTranscript(t, cl, "vậy được rồi")
	once.Do(func() { close(release) })
	select {
	case err := <-opened:
		if err == nil {
			t.Fatal("follow-up racing with closing was accepted")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("closing left the opening follow-up stuck")
	}
	cl.waitHeld(t, false, "the racing follow-up returned its microphone")
	if cl.f.Listening() {
		t.Fatal("racing follow-up remained Listening")
	}
	YieldMic(old)
	if err := cl.f.Wake(); err != nil {
		t.Fatalf("the next explicit wake was cancelled or refused: %v", err)
	}
	cl.waitHeld(t, true, "the next explicit wake can reclaim the microphone")
	cl.speech(t, 25)
	cl.cloud.waitAudio(t, "the new wake still sends audio after the old follow-up ended", 8)
	if !cl.f.Listening() {
		t.Fatal("old follow-up cancellation ended the later explicit wake")
	}
}
