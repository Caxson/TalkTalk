package dialog

import (
	"strings"
	"unicode"
)

// fillers are backchannel sounds that should not interrupt the assistant.
var fillers = map[rune]bool{
	'嗯': true, '啊': true, '哦': true, '噢': true, '呃': true, '额': true, '唔': true,
	'哈': true, '呀': true, '嘛': true, '诶': true, '欸': true, '哎': true,
}

// substantive counts the runes that carry content: letters and digits
// that are not filler sounds.
func substantive(text string) int {
	n := 0
	for _, r := range text {
		if (unicode.IsLetter(r) || unicode.IsDigit(r)) && !fillers[unicode.ToLower(r)] {
			n++
		}
	}
	return n
}

// joinUtterances concatenates the user's consecutive sentences.
func joinUtterances(parts []string) string {
	return strings.Join(parts, "")
}

// normalizeText keeps only letters and digits, so interim and final
// transcripts of the same words compare equal despite punctuation.
func normalizeText(text string) string {
	var b strings.Builder
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// addedRunes counts how many characters now extends beyond was. If now is
// not a continuation of was, every character of now counts as new.
func addedRunes(was, now string) int {
	if strings.HasPrefix(now, was) {
		return len([]rune(now)) - len([]rune(was))
	}
	return len([]rune(now))
}
