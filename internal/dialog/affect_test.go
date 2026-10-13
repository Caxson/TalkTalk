package dialog

import (
	"strings"
	"testing"
	"time"

	"github.com/Caxson/TalkTalk/internal/llm"
)

// scan feeds text one rune at a time, the worst case for split tags.
func scan(text string, dropUnknown bool) string {
	s := tagScanner{dropUnknown: dropUnknown}
	var b strings.Builder
	for _, r := range text {
		b.WriteString(s.Push(string(r)))
	}
	return b.String() + s.Flush()
}

func TestTagScanner(t *testing.T) {
	long := "[" + strings.Repeat("a", maxTagRunes+2) + "]"
	cases := []struct {
		in, text string
		last     Affect
		drop     bool
	}{
		{"[happy]好呀！", "好呀！", AffectHappy, true},
		{"[Happy]好呀！", "好呀！", AffectHappy, true},
		{"【happy】好呀！", "好呀！", AffectHappy, true}, // full-width brackets
		{"［sad］唉。", "唉。", AffectSad, true},
		{"[ thinking ]嗯。", "嗯。", AffectThinking, true}, // spaces inside
		{"好的[thinking]让我想想。", "好的让我想想。", AffectThinking, true},
		{"[wink]眨眼。", "眨眼。", "", true},                // unknown tag, model was taught: dropped
		{"选 [A] 还是 [B]？", "选 [A] 还是 [B]？", "", false}, // not taught: plain text
		{"选 [A] 还是 [OK]？", "选  还是 ？", "", true},
		{"数组 a[3] 和 [链接]。", "数组 a[3] 和 [链接]。", "", true},
		{"【注意】别忘了。", "【注意】别忘了。", "", true},
		{"最后是[happy", "最后是[happy", "", true}, // unfinished at the end
		{"[[sad]呜", "[呜", AffectSad, true},
		{long, long, "", true},
	}
	for _, c := range cases {
		text, last, ok := splitAffect(scan(c.in, c.drop))
		if text != c.text || last != c.last || ok != (c.last != "") {
			t.Errorf("%q (drop %v) → text %q affect %q (ok %v), want %q %q", c.in, c.drop, text, last, ok, c.text, c.last)
		}
	}
}

func TestTaggedForHistory(t *testing.T) {
	if s := (spoken{"好呀！", AffectHappy}).tagged(); s != "[happy]好呀！" {
		t.Error(s)
	}
	if s := (spoken{"好呀！", ""}).tagged(); s != "好呀！" {
		t.Error(s)
	}
}

func TestAffectReachesClientAndHistory(t *testing.T) {
	h := newHarnessWith(t, func(c *Config) { c.Affect = true }, time.Millisecond, "[happy]太好了！[thinking]不过我得想想。")
	h.say("我升职了", true)
	ev := h.waitFor("happy segment", func(e Event) bool { return e.Type == "assistant" && e.Text == "太好了！" })
	if ev.Affect != AffectHappy {
		t.Errorf("first segment affect = %q", ev.Affect)
	}
	ev = h.waitFor("thinking segment", func(e Event) bool { return e.Type == "assistant" && e.Text == "不过我得想想。" })
	if ev.Affect != AffectThinking {
		t.Errorf("second segment affect = %q", ev.Affect)
	}
	h.waitState(StateListening)

	sys := h.llm.call(0)[0]
	if !strings.HasPrefix(sys.Content, "SYS\n") || !strings.Contains(sys.Content, "[happy]") {
		t.Errorf("system prompt lacks the affect instruction: %q", sys.Content)
	}
	want := []string{"user:我升职了", "assistant:[happy]太好了！[thinking]不过我得想想。"}
	if got := texts(h.stop()); !equal(got, want) {
		t.Errorf("history = %q, want %q", got, want)
	}
}

func TestTagOnlyFirstSegmentKeepsTheEarlyCut(t *testing.T) {
	h := newHarnessWith(t, func(c *Config) { c.Affect = true }, time.Millisecond, "[happy]\n今天天气很好，我们出去走走吧。[sad]")
	h.say("天气怎么样", true)
	ev := h.waitFor("first segment", func(e Event) bool { return e.Type == "assistant" })
	if ev.Text != "今天天气很好，" || ev.Affect != AffectHappy {
		t.Errorf("first segment = %q (%q), want the early comma cut carrying happy", ev.Text, ev.Affect)
	}
	ev = h.waitFor("second segment", func(e Event) bool { return e.Type == "assistant" })
	if ev.Text != "我们出去走走吧。" || ev.Affect != "" {
		t.Errorf("second segment = %q (%q)", ev.Text, ev.Affect)
	}
	h.waitState(StateListening)
	want := []string{"user:天气怎么样", "assistant:[happy]今天天气很好，我们出去走走吧。"}
	if got := texts(h.stop()); !equal(got, want) {
		t.Errorf("history = %q, want %q (a trailing tag is never heard)", got, want)
	}
}

func TestAffectWithoutSystemPrompt(t *testing.T) {
	h := newHarnessWith(t, func(c *Config) { c.Affect, c.SystemPrompt = true, "" }, time.Millisecond, "好。")
	h.say("你好", true)
	h.waitState(StateSpeaking)
	h.waitState(StateListening)
	if sys := h.llm.call(0)[0]; sys.Role != llm.RoleSystem || sys.Content != affectInstruction {
		t.Errorf("system message = %+v", sys)
	}
}

func TestAffectOffKeepsPromptAndStillStripsTags(t *testing.T) {
	h := newHarness(t, time.Millisecond, "[happy]好呀。")
	h.say("你好", true)
	ev := h.waitFor("segment", func(e Event) bool { return e.Type == "assistant" })
	if ev.Text != "好呀。" {
		t.Errorf("a stray tag reached speech: %q", ev.Text)
	}
	h.waitState(StateListening)
	if got := texts(h.llm.call(0)); !equal(got, []string{"system:SYS", "user:你好"}) {
		t.Errorf("prompt = %q", got)
	}
}
