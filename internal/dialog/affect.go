package dialog

import (
	"strings"
	"unicode/utf8"
)

// Affect is the feeling a reply segment is spoken with. The model marks it
// with an inline tag such as "[happy]"; the tag is never spoken, and the
// client turns it into a facial expression timed with the segment's audio.
type Affect string

// The affects the client knows how to show.
const (
	AffectNeutral   Affect = "neutral"
	AffectHappy     Affect = "happy"
	AffectSurprised Affect = "surprised"
	AffectThinking  Affect = "thinking"
	AffectShy       Affect = "shy"
	AffectSad       Affect = "sad"
)

var affects = map[string]Affect{
	"neutral": AffectNeutral, "happy": AffectHappy, "surprised": AffectSurprised,
	"thinking": AffectThinking, "shy": AffectShy, "sad": AffectSad,
}

// affectInstruction teaches the model the tags; it is appended to the
// system prompt when Config.Affect is on.
const affectInstruction = "表情：你有一张会动的脸。每轮回复的开头必须先写一个英文情绪标签，表示你说这段话时的心情；" +
	"句中情绪明显变化时，再在那句话前加一个。只能用这几个：[happy] [surprised] [thinking] [shy] [sad] [neutral]。" +
	"标签不会被念出来，只用来控制你的表情，情绪要和内容一致。"

// maxTagRunes bounds a tag's name; longer bracketed text is ordinary text.
const maxTagRunes = 16

// affectMark stands in for a tag inside the text stream, so the affect
// travels through segmentation at its exact position. The private-use
// runes are never letters, so the segmenter does not count them as speech.
const affectMark = ''

var markOf, affectOfMark = func() (map[Affect]rune, map[rune]Affect) {
	names := []Affect{AffectNeutral, AffectHappy, AffectSurprised, AffectThinking, AffectShy, AffectSad}
	to, from := map[Affect]rune{}, map[rune]Affect{}
	for i, a := range names {
		to[a], from[affectMark+rune(i)] = affectMark+rune(i), a
	}
	return to, from
}()

// tagBrackets pairs the brackets a tag may use: models writing Chinese
// often switch to the full-width forms.
var tagBrackets = map[rune]rune{'[': ']', '【': '】', '［': '］'}

// tagScanner replaces "[name]" tags in streamed text with affect marks.
// With dropUnknown (the model was taught the tags), other well-formed
// "[word]" tags are dropped rather than read aloud; without it they are
// ordinary text, like "[A]" or "[OK]". Anything that is not a tag passes
// through unchanged. Tags may be split across deltas.
type tagScanner struct {
	dropUnknown bool
	pending     []rune // an open "[..." not yet known to be a tag
	close       rune   // the bracket that would close it
}

// Push returns the text that is now safe to pass on.
func (s *tagScanner) Push(delta string) string {
	var out strings.Builder
	for _, r := range delta {
		if len(s.pending) == 0 {
			if c, ok := tagBrackets[r]; ok {
				s.pending, s.close = append(s.pending, r), c
			} else {
				out.WriteRune(r)
			}
			continue
		}
		switch {
		case r == s.close:
			name := strings.TrimSpace(string(s.pending[1:]))
			if a, ok := affects[strings.ToLower(name)]; ok {
				out.WriteRune(markOf[a])
			} else if !s.dropUnknown || !isTagName([]rune(name)) {
				out.WriteString(string(s.pending) + string(r))
			} // a well-formed unknown tag is dropped
			s.pending = s.pending[:0]
		case (isTagRune(r) || r == ' ') && len(s.pending) <= maxTagRunes:
			s.pending = append(s.pending, r)
		default:
			// Not a tag after all: release it, then reconsider r.
			out.WriteString(string(s.pending))
			s.pending = s.pending[:0]
			out.WriteString(s.Push(string(r)))
		}
	}
	return out.String()
}

// Flush releases an unfinished "[...".
func (s *tagScanner) Flush() string {
	out := string(s.pending)
	s.pending = nil
	return out
}

func isTagRune(r rune) bool {
	return r < utf8.RuneSelf && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '_')
}

func isTagName(rs []rune) bool {
	if len(rs) == 0 {
		return false
	}
	for _, r := range rs {
		if !isTagRune(r) {
			return false
		}
	}
	return true
}

// splitAffect removes affect marks from a segment. It returns the clean
// text and the last affect marked in it (ok false if none).
func splitAffect(seg string) (text string, last Affect, ok bool) {
	var b strings.Builder
	for _, r := range seg {
		if a, isMark := affectOfMark[r]; isMark {
			last, ok = a, true
			continue
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String()), last, ok
}

// spoken is one speakable segment and the affect it carries.
type spoken struct {
	text   string
	affect Affect // empty: unchanged from the previous segment
}

// tagged renders a segment the way the model wrote it, for history, so the
// model keeps seeing its own tags.
func (s spoken) tagged() string {
	if s.affect == "" {
		return s.text
	}
	return "[" + string(s.affect) + "]" + s.text
}
