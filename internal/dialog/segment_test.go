package dialog

import (
	"reflect"
	"strings"
	"testing"
)

func feed(s *Segmenter, deltas ...string) []string {
	var out []string
	for _, d := range deltas {
		out = append(out, s.Push(d)...)
	}
	if tail, ok := s.Flush(); ok {
		out = append(out, tail)
	}
	return out
}

func TestSegmentsOnSentenceEnds(t *testing.T) {
	got := feed(NewSegmenter(), "今天天气", "很好。我们去", "公园吧！好不好？")
	want := []string{"今天天气很好。", "我们去公园吧！", "好不好？"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFirstSegmentCutsEarlyAtComma(t *testing.T) {
	got := feed(NewSegmenter(), "好的，没问题，", "我这就帮你查一下明天的天气情况，然后告诉你。")
	// First segment cuts at the first comma once long enough; later ones
	// wait for a sentence end.
	want := []string{"好的，没问题，", "我这就帮你查一下明天的天气情况，然后告诉你。"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFirstSegmentTooShortWaits(t *testing.T) {
	got := feed(NewSegmenter(), "嗯，", "我觉得可以。")
	want := []string{"嗯，我觉得可以。"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLongRunWithoutPunctuationIsForcedAtWeakBreak(t *testing.T) {
	long := strings.Repeat("长", 50) + "，" + strings.Repeat("句", 30)
	got := feed(NewSegmenter(), "开头。", long, "。")
	if len(got) != 3 || got[1] != strings.Repeat("长", 50)+"，" {
		t.Errorf("got %q", got)
	}
}

func TestHardCutWhenNoBreakAtAll(t *testing.T) {
	s := NewSegmenter()
	s.Push("开头。")
	got := s.Push(strings.Repeat("字", 100))
	if len(got) != 1 || len([]rune(got[0])) != maxSegmentRunes {
		t.Errorf("got %d segments, first len %d", len(got), len([]rune(got[0])))
	}
}

func TestStripsMarkdownAndSkipsEmpty(t *testing.T) {
	got := feed(NewSegmenter(), "**重点**：", "第一，`代码`。", "\n\n", "## 标题\n", "。。")
	// "重点：第一，" has 4 speakable runes, enough for an early first cut.
	want := []string{"重点：第一，", "代码。", "标题"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestEnglishPunctuation(t *testing.T) {
	got := feed(NewSegmenter(), "Sure thing. Let's go! Ready?")
	want := []string{"Sure thing.", "Let's go!", "Ready?"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDecimalPointDoesNotSplit(t *testing.T) {
	got := feed(NewSegmenter(), "气温是 3.5 度。")
	want := []string{"气温是 3.5 度。"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFlushEmpty(t *testing.T) {
	if _, ok := NewSegmenter().Flush(); ok {
		t.Error("Flush on empty segmenter returned a segment")
	}
}
