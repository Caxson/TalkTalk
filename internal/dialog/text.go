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

// substantive counts units of content: each Han (or other non-Latin)
// character is one unit, each Latin word or number is one unit, filler
// sounds count nothing. "Hi" is one unit, like "嗨".
func substantive(text string) int {
	n, inWord := 0, false
	for _, r := range text {
		switch {
		case r < 0x250 && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			if !inWord {
				n++
				inWord = true
			}
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			inWord = false
			if !fillers[r] {
				n++
			}
		default:
			inWord = false
		}
	}
	return n
}

// joinUtterances concatenates the user's consecutive sentences.
func joinUtterances(parts []string) string {
	return strings.Join(parts, "")
}

// normalizeText keeps only letters and digits, lower-cased, so transcripts
// of the same words compare equal despite punctuation, spacing and case
// ("TalkTalk" and "talk talk").
func normalizeText(text string) string {
	var b strings.Builder
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
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

// soundsLikeOwnWords reports whether heard is the assistant's own speech
// picked up again by the microphone. recent is what it said in its last
// couple of segments; all is everything it has said this turn.
//
// Echo is usually recognized in short fragments ("嗨，你" while it says
// "嗨你好呀"), so two or three characters (fillers removed) are echo when
// they occur verbatim in what was just said; longer text is echo when most
// of its character bigrams appear in what was said this turn.
func soundsLikeOwnWords(heard, recent, all string) bool {
	h := []rune(stripFillers(normalizeText(heard)))
	switch {
	case len(h) < 2:
		return false
	case len(h) < 4:
		return strings.Contains(normalizeText(recent), string(h))
	}
	s := []rune(normalizeText(all))
	own := make(map[[2]rune]bool, len(s))
	for i := 1; i < len(s); i++ {
		own[[2]rune{s[i-1], s[i]}] = true
	}
	hits := 0
	for i := 1; i < len(h); i++ {
		if own[[2]rune{h[i-1], h[i]}] {
			hits++
		}
	}
	return float64(hits) >= 0.6*float64(len(h)-1)
}

// stripFillers drops backchannel sounds (嗯, 啊, 呃...).
func stripFillers(s string) string {
	var b strings.Builder
	for _, r := range s {
		if !fillers[unicode.ToLower(r)] {
			b.WriteRune(r)
		}
	}
	return b.String()
}
