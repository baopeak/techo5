package xiaozhi

import (
	"errors"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

var errConversationEnded = errors.New("the conversation has ended; a wake word starts another")

// isConversationEnd recognizes explicit closing utterances, not phrases embedded
// in a request. An ordinary thank-you or agreement does not end the conversation.
func isConversationEnd(text string) bool {
	if strings.ContainsAny(text, "?？") {
		return false
	}
	text = norm.NFC.String(text)
	for _, phrase := range [...]string{
		"vậy được rồi", "vậy là được rồi", "thôi được rồi",
		"vậy được rồi cảm ơn", "vậy được rồi cảm ơn bạn",
		"vậy được rồi cảm ơn đệ",
		"được rồi cảm ơn", "được rồi cảm ơn bạn", "được rồi cảm ơn đệ",
		"tạm biệt", "dừng trò chuyện", "kết thúc trò chuyện",
	} {
		remaining := phrase
		matches := true
		for word := range strings.FieldsFuncSeq(text, func(r rune) bool {
			return unicode.IsSpace(r) || unicode.IsPunct(r)
		}) {
			if remaining == "" {
				matches = false
				break
			}
			expected, rest, _ := strings.Cut(remaining, " ")
			if !strings.EqualFold(word, expected) {
				matches = false
				break
			}
			remaining = rest
		}
		if matches && remaining == "" {
			return true
		}
	}
	return false
}
