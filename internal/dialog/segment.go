package dialog

import (
	"strings"
	"unicode"
)

const (
	// firstSegmentMinRunes lets the first segment end at a comma so the
	// first audio starts early; later segments wait for a sentence end,
	// which gives the TTS better prosody.
	firstSegmentMinRunes = 4
	// weakBreakMinRunes allows a comma break once a sentence runs long.
	weakBreakMinRunes = 40
	maxSegmentRunes   = 80
)

func isStrongBreak(r rune) bool {
	switch r {
	case '。', '！', '？', '；', '!', '?', ';', '\n', '…':
		return true
	}
	return false
}

func isWeakBreak(r rune) bool {
	switch r {
	case '，', '、', '：', ',', ':':
		return true
	}
	return false
}

// markdownNoise is stripped because it would be read aloud or garbled.
var markdownNoise = strings.NewReplacer("*", "", "#", "", "`", "", "_", "", ">", "")

// Segmenter splits a streamed reply into speakable segments. Not safe for
// concurrent use.
type Segmenter struct {
	buf     []rune
	emitted int
}

// NewSegmenter returns an empty Segmenter.
func NewSegmenter() *Segmenter { return &Segmenter{} }

// Push adds a delta and returns segments that are now complete.
func (s *Segmenter) Push(delta string) []string {
	s.buf = append(s.buf, []rune(markdownNoise.Replace(delta))...)
	var out []string
	for {
		cut := s.cutPoint()
		if cut == 0 {
			return out
		}
		seg := string(s.buf[:cut])
		s.buf = s.buf[cut:]
		if seg = clean(seg); seg != "" {
			out = append(out, seg)
			s.emitted++
		}
	}
}

// Flush returns whatever remains as a final segment.
func (s *Segmenter) Flush() (string, bool) {
	seg := clean(string(s.buf))
	s.buf = nil
	if seg == "" {
		return "", false
	}
	s.emitted++
	return seg, true
}

// cutPoint returns the length of the next complete segment, or 0.
func (s *Segmenter) cutPoint() int {
	lastWeak := 0
	for i, r := range s.buf {
		n := i + 1
		switch {
		case r == '.':
			// A period ends a sentence only when followed by a space or
			// end of text; "3.5" must stay intact. At the buffer end we
			// cannot know yet, so wait.
			if i+1 < len(s.buf) && unicode.IsSpace(s.buf[i+1]) {
				return n
			}
		case isStrongBreak(r):
			return n
		case isWeakBreak(r):
			if s.emitted == 0 && speakable(s.buf[:n]) >= firstSegmentMinRunes {
				return n
			}
			lastWeak = n
		}
		if n >= weakBreakMinRunes && lastWeak > 0 && isWeakBreak(r) {
			return lastWeak
		}
		if n >= maxSegmentRunes {
			if lastWeak > 0 {
				return lastWeak
			}
			return n
		}
	}
	return 0
}

// speakable counts letters and digits, ignoring punctuation and spaces.
func speakable(rs []rune) int {
	n := 0
	for _, r := range rs {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			n++
		}
	}
	return n
}

// clean trims whitespace and drops segments with nothing to pronounce.
func clean(seg string) string {
	seg = strings.TrimSpace(seg)
	if speakable([]rune(seg)) == 0 {
		return ""
	}
	return seg
}
