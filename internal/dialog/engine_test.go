package dialog

import (
	"context"
	"errors"
	"io"
	"iter"
	"log/slog"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Caxson/TalkTalk/internal/asr"
	"github.com/Caxson/TalkTalk/internal/llm"
	"github.com/Caxson/TalkTalk/internal/playout"
)

// ---- fakes ----

type fakeStream struct {
	results chan asr.Result
	mu      sync.Mutex
	writes  int
	err     error
}

func (s *fakeStream) Write(context.Context, []int16) error {
	s.mu.Lock()
	s.writes++
	s.mu.Unlock()
	return nil
}
func (s *fakeStream) Results() <-chan asr.Result { return s.results }
func (s *fakeStream) Err() error                 { return s.err }
func (s *fakeStream) Close() error               { return nil }

type fakeRecognizer struct {
	mu      sync.Mutex
	streams []*fakeStream
	opened  chan *fakeStream
}

func newFakeRecognizer() *fakeRecognizer {
	return &fakeRecognizer{opened: make(chan *fakeStream, 8)}
}

func (r *fakeRecognizer) SampleRate() int { return 16000 }
func (r *fakeRecognizer) Open(context.Context) (asr.Stream, error) {
	s := &fakeStream{results: make(chan asr.Result, 16)}
	r.mu.Lock()
	r.streams = append(r.streams, s)
	r.mu.Unlock()
	r.opened <- s
	return s, nil
}

// fakeLLM replies with a scripted answer per call. A reply of "BLOCK"
// waits for cancellation; "FAIL" errors after one delta.
type fakeLLM struct {
	mu      sync.Mutex
	replies []string
	calls   [][]llm.Message
	started chan struct{}
}

func (f *fakeLLM) Stream(ctx context.Context, msgs []llm.Message) iter.Seq2[string, error] {
	f.mu.Lock()
	f.calls = append(f.calls, append([]llm.Message(nil), msgs...))
	reply := "好的。"
	if len(f.replies) > 0 {
		reply, f.replies = f.replies[0], f.replies[1:]
	}
	f.mu.Unlock()
	if f.started != nil {
		f.started <- struct{}{}
	}
	return func(yield func(string, error) bool) {
		switch reply {
		case "BLOCK":
			<-ctx.Done()
			yield("", ctx.Err())
			return
		case "FAIL":
			yield("", errors.New("provider overloaded"))
			return
		case "LEAK":
			// Buffered output keeps coming after cancellation, as an
			// SSE body read from a bufio buffer does.
			if !yield("第一句。", nil) {
				return
			}
			<-ctx.Done()
			for _, d := range []string{"第二句。", "第三句。", "第四句。"} {
				if !yield(d, nil) {
					return
				}
			}
			return
		}
		for _, r := range reply {
			if ctx.Err() != nil {
				yield("", ctx.Err())
				return
			}
			if !yield(string(r), nil) {
				return
			}
		}
	}
}

func (f *fakeLLM) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeLLM) call(i int) []llm.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i >= len(f.calls) {
		return nil
	}
	return f.calls[i]
}

// fakeTTS produces perRune of audio per character, in 20ms chunks. With
// silentCancel it stops without an error on cancellation, as the real
// DashScope client does.
type fakeTTS struct {
	perRune      time.Duration
	silentCancel bool
	realTime     bool // emit audio at playback speed instead of instantly
}

func (f fakeTTS) SampleRate() int { return 16000 }
func (f fakeTTS) Synthesize(ctx context.Context, text string) iter.Seq2[[]int16, error] {
	return func(yield func([]int16, error) bool) {
		total := int(f.perRune) * len([]rune(text)) / int(time.Second/16000)
		for total > 0 {
			if ctx.Err() != nil {
				if !f.silentCancel {
					yield(nil, ctx.Err())
				}
				return
			}
			n := min(320, total)
			total -= n
			if !yield(make([]int16, n), nil) {
				return
			}
			if f.realTime {
				select {
				case <-ctx.Done():
				case <-time.After(20 * time.Millisecond):
				}
			}
		}
	}
}

// ---- harness ----

// gainPlayer records the requested gain on top of a real player.
type gainPlayer struct {
	*playout.Player
	mu   sync.Mutex
	gain float64
}

func (g *gainPlayer) SetGain(v float64) {
	g.mu.Lock()
	g.gain = v
	g.mu.Unlock()
	g.Player.SetGain(v)
}

func (g *gainPlayer) Flush() []int {
	g.mu.Lock()
	g.gain = 1
	g.mu.Unlock()
	return g.Player.Flush()
}

func (g *gainPlayer) gainTarget() float64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.gain
}

type harness struct {
	t      *testing.T
	player *gainPlayer
	eng    *Engine
	rec    *fakeRecognizer
	llm    *fakeLLM
	stream *fakeStream
	events chan Event
	cancel context.CancelFunc
	done   chan error
	once   sync.Once
	runErr error
}

func newHarness(t *testing.T, perRune time.Duration, replies ...string) *harness {
	t.Helper()
	return newHarnessWith(t, nil, perRune, replies...)
}

func newHarnessWith(t *testing.T, tune func(*Config), perRune time.Duration, replies ...string) *harness {
	return newHarnessTTS(t, tune, fakeTTS{perRune: perRune}, replies...)
}

func newHarnessTTS(t *testing.T, tune func(*Config), synth fakeTTS, replies ...string) *harness {
	t.Helper()
	player, err := playout.New(16000, func([]byte, time.Duration) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go player.Run(ctx)

	h := &harness{
		t:      t,
		player: &gainPlayer{Player: player, gain: 1},
		rec:    newFakeRecognizer(),
		llm:    &fakeLLM{replies: replies},
		events: make(chan Event, 1024),
		cancel: cancel,
		done:   make(chan error, 1),
	}
	cfg := Config{SystemPrompt: "SYS", ASRReopenDelay: 10 * time.Millisecond}
	if tune != nil {
		tune(&cfg)
	}
	h.eng, err = New(cfg, Deps{
		Recognizer: h.rec,
		LLM:        h.llm,
		TTS:        synth,
		Player:     h.player,
		Notify:     func(e Event) { h.events <- e },
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() { h.done <- h.eng.Run(ctx) }()
	h.stream = <-h.rec.opened
	// Open() returns before the engine publishes the stream to PushAudio.
	for h.eng.stream.Load() == nil {
		time.Sleep(time.Millisecond)
	}
	t.Cleanup(func() { h.shutdown() })
	return h
}

func (h *harness) say(text string, final bool) {
	h.stream.results <- asr.Result{SentenceID: 1, Text: text, Final: final}
}

// waitFor consumes events until pred matches, failing after a timeout.
func (h *harness) waitFor(desc string, pred func(Event) bool) Event {
	h.t.Helper()
	timeout := time.After(3 * time.Second)
	for {
		select {
		case e := <-h.events:
			if pred(e) {
				return e
			}
		case <-timeout:
			h.t.Fatalf("timed out waiting for %s", desc)
			return Event{}
		}
	}
}

func (h *harness) waitState(s State) {
	h.t.Helper()
	h.waitFor("state "+string(s), func(e Event) bool { return e.Type == "state" && e.State == s })
}

func (h *harness) shutdown() {
	h.once.Do(func() {
		h.cancel()
		h.runErr = <-h.done
	})
}

// stop ends Run and returns the committed history.
func (h *harness) stop() []llm.Message {
	h.shutdown()
	return h.eng.History()
}

func texts(msgs []llm.Message) []string {
	var out []string
	for _, m := range msgs {
		out = append(out, string(m.Role)+":"+m.Content)
	}
	return out
}

func equal(a, b []string) bool { return strings.Join(a, "|") == strings.Join(b, "|") }

// ---- tests ----

func TestFullTurn(t *testing.T) {
	h := newHarness(t, time.Millisecond, "你好呀。今天怎么样？")
	h.waitState(StateListening)
	h.say("你好", true)

	h.waitState(StateThinking)
	h.waitState(StateSpeaking)
	m := h.waitFor("metrics", func(e Event) bool { return e.Type == "metrics" })
	if m.Metrics.LLMFirstToken <= 0 || m.Metrics.FirstAudioOut < m.Metrics.LLMFirstToken {
		t.Errorf("implausible metrics %+v", m.Metrics)
	}
	h.waitState(StateListening)

	want := []string{"user:你好", "assistant:你好呀。今天怎么样？"}
	if got := texts(h.stop()); !equal(got, want) {
		t.Errorf("history = %q, want %q", got, want)
	}
	if got := texts(h.llm.call(0)); !equal(got, []string{"system:SYS", "user:你好"}) {
		t.Errorf("prompt = %q", got)
	}
}

func TestSecondTurnSeesHistory(t *testing.T) {
	h := newHarness(t, time.Millisecond, "第一。", "第二。")
	h.say("一", true)
	h.waitState(StateSpeaking)
	h.waitState(StateListening)
	h.say("二", true)
	h.waitState(StateSpeaking)
	h.waitState(StateListening)

	want := []string{"system:SYS", "user:一", "assistant:第一。", "user:二"}
	if got := texts(h.llm.call(1)); !equal(got, want) {
		t.Errorf("second prompt = %q, want %q", got, want)
	}
}

func TestUserContinuingRetractsAndMerges(t *testing.T) {
	h := newHarness(t, time.Millisecond, "BLOCK", "好的，帮你订。")
	h.llm.started = make(chan struct{}, 4)
	h.say("帮我", true)
	h.waitState(StateThinking)
	<-h.llm.started

	h.say("订一张", false) // user keeps talking before any audio
	h.waitState(StateListening)
	h.say("订一张票", true)
	h.waitState(StateSpeaking)
	h.waitState(StateListening)

	if got := texts(h.llm.call(1)); !equal(got, []string{"system:SYS", "user:帮我订一张票"}) {
		t.Errorf("merged prompt = %q", got)
	}
	want := []string{"user:帮我订一张票", "assistant:好的，帮你订。"}
	if got := texts(h.stop()); !equal(got, want) {
		t.Errorf("history = %q, want %q", got, want)
	}
}

func TestBargeInKeepsOnlyWhatWasHeard(t *testing.T) {
	// 100ms per rune: the reply takes seconds, so we interrupt mid-way.
	h := newHarness(t, 100*time.Millisecond, "第一句话。第二句很长很长很长很长。第三句。")
	h.say("讲个故事", true)
	h.waitState(StateSpeaking)
	h.waitFor("first segment played", func(e Event) bool { return e.Type == "assistant" })
	time.Sleep(600 * time.Millisecond) // let "第一句话。" finish playing

	h.say("等一下", false)
	h.waitFor("interrupted", func(e Event) bool { return e.Type == "interrupted" })
	h.waitState(StateListening)

	hist := texts(h.stop())
	if len(hist) != 2 || hist[0] != "user:讲个故事" ||
		!strings.HasPrefix(hist[1], "assistant:第一句话。") || !strings.Contains(hist[1], "打断") ||
		strings.Contains(hist[1], "第三句") {
		t.Errorf("history = %q", hist)
	}
}

func TestBackchannelDoesNotInterrupt(t *testing.T) {
	h := newHarness(t, 20*time.Millisecond, "这是一个比较长的回答，需要说一会儿才能说完。")
	h.say("问题", true)
	h.waitState(StateSpeaking)
	h.say("嗯", false)
	h.say("嗯嗯。", true)
	h.say("好", true) // one substantive rune: below the interrupt threshold
	h.waitState(StateListening)

	want := []string{"user:问题", "assistant:这是一个比较长的回答，需要说一会儿才能说完。"}
	if got := texts(h.stop()); !equal(got, want) {
		t.Errorf("history = %q, want %q", got, want)
	}
}

func TestLLMFailureKeepsUserTextForRetry(t *testing.T) {
	h := newHarness(t, time.Millisecond, "FAIL", "现在可以了。")
	h.say("你好", true)
	h.waitFor("error", func(e Event) bool { return e.Type == "error" })
	h.waitState(StateListening)

	h.say("在吗", true)
	h.waitState(StateSpeaking)
	h.waitState(StateListening)
	if got := texts(h.llm.call(1)); !equal(got, []string{"system:SYS", "user:你好在吗"}) {
		t.Errorf("retry prompt = %q", got)
	}
}

func TestASRStreamFailureReopens(t *testing.T) {
	h := newHarness(t, time.Millisecond, "收到。")
	h.stream.err = errors.New("network down")
	close(h.stream.results)

	select {
	case s := <-h.rec.opened:
		h.stream = s
	case <-time.After(2 * time.Second):
		t.Fatal("ASR stream was not reopened")
	}
	h.say("还在吗", true)
	h.waitState(StateSpeaking)
	h.waitState(StateListening)
}

func TestPushAudioReachesStream(t *testing.T) {
	h := newHarness(t, time.Millisecond)
	for i := 0; i < 5; i++ {
		h.eng.PushAudio(context.Background(), make([]int16, 320))
	}
	h.stream.mu.Lock()
	defer h.stream.mu.Unlock()
	if h.stream.writes != 5 {
		t.Errorf("writes = %d, want 5", h.stream.writes)
	}
}

func TestNewRequiresDeps(t *testing.T) {
	if _, err := New(Config{}, Deps{}); err == nil {
		t.Error("New without deps succeeded")
	}
}

func TestTranscriptFollowsPlayback(t *testing.T) {
	// 100ms of audio per rune: "第一句话。" plays for 0.5s.
	h := newHarness(t, 100*time.Millisecond, "第一句话。第二句话。")
	h.say("说两句", true)
	first := h.waitFor("first segment", func(e Event) bool { return e.Type == "assistant" })
	second := h.waitFor("second segment", func(e Event) bool { return e.Type == "assistant" })
	if first.Text != "第一句话。" || second.Text != "第二句话。" {
		t.Fatalf("segments = %q, %q", first.Text, second.Text)
	}
	if gap := second.Timestamp.Sub(first.Timestamp); gap < 400*time.Millisecond {
		t.Errorf("second segment shown %v after the first; want ≈ its 500ms playback", gap)
	}
}

func TestTurnLifecycleEvents(t *testing.T) {
	h := newHarness(t, 20*time.Millisecond, "BLOCK", "这是一个需要说上一阵子的回答，对吧。")
	h.llm.started = make(chan struct{}, 4)
	h.say("帮我", true)
	t1 := h.waitFor("turn 1", func(e Event) bool { return e.Type == "turn" })
	<-h.llm.started
	h.say("看看", false)
	r := h.waitFor("retracted", func(e Event) bool { return e.Type == "retracted" })
	if r.Turn != t1.Turn {
		t.Errorf("retracted turn %d, want %d", r.Turn, t1.Turn)
	}
	h.say("看看这个", true)
	t2 := h.waitFor("turn 2", func(e Event) bool { return e.Type == "turn" })
	if t2.Text != "帮我看看这个" || t2.Turn == t1.Turn {
		t.Errorf("turn 2 = %+v, want merged text and a new id", t2)
	}
	h.waitState(StateSpeaking)
	h.say("嗯嗯。", true)
	bc := h.waitFor("backchannel", func(e Event) bool { return e.Type == "backchannel" })
	if bc.Turn != t2.Turn || bc.Text != "嗯嗯。" {
		t.Errorf("backchannel = %+v", bc)
	}
	h.waitState(StateListening)
}

func newPartialHarness(t *testing.T, replies ...string) *harness {
	t.Helper()
	return newHarnessWith(t, func(c *Config) { c.PartialEndpointTimeout = 150 * time.Millisecond }, time.Millisecond, replies...)
}

func (h *harness) sayID(id int, text string, final bool) {
	h.stream.results <- asr.Result{SentenceID: id, Text: text, Final: final}
}

func TestStalledPartialStartsTurn(t *testing.T) {
	// The recognizer never closes the sentence: the engine must not wait
	// for the user to speak again.
	h := newPartialHarness(t, "北京好玩的地方很多。")
	h.sayID(5, "介绍一下北京", false)
	h.sayID(5, "介绍一下北京有哪些好玩的地方", false)
	turn := h.waitFor("turn from stalled partial", func(e Event) bool { return e.Type == "turn" })
	if turn.Text != "介绍一下北京有哪些好玩的地方" {
		t.Errorf("turn text = %q", turn.Text)
	}
	// A repeated identical partial is not "still speaking".
	h.sayID(5, "介绍一下北京有哪些好玩的地方", false)
	// The late final with the same words confirms the turn instead of
	// retracting or duplicating it.
	h.sayID(5, "介绍一下北京有哪些好玩的地方？", true)
	h.waitState(StateListening)

	want := []string{"user:介绍一下北京有哪些好玩的地方？", "assistant:北京好玩的地方很多。"}
	if got := texts(h.stop()); !equal(got, want) {
		t.Errorf("history = %q, want %q", got, want)
	}
	if n := h.llm.count(); n != 1 {
		t.Errorf("LLM called %d times, want 1", n)
	}
}

func TestLateFinalWithDifferentWordsRestartsSilentTurn(t *testing.T) {
	h := newPartialHarness(t, "BLOCK", "订好了。")
	h.llm.started = make(chan struct{}, 4)
	h.sayID(2, "帮我订明天", false)
	h.waitFor("turn", func(e Event) bool { return e.Type == "turn" })
	<-h.llm.started
	h.sayID(2, "帮我订明天的票。", true)
	h.waitFor("retracted", func(e Event) bool { return e.Type == "retracted" })
	t2 := h.waitFor("corrected turn", func(e Event) bool { return e.Type == "turn" })
	if t2.Text != "帮我订明天的票。" {
		t.Errorf("corrected turn = %q", t2.Text)
	}
	h.waitState(StateListening)
}

func TestNormalFinalBeforeTimeoutDoesNotDoubleStart(t *testing.T) {
	h := newPartialHarness(t, "好。")
	h.sayID(1, "你好", false)
	h.sayID(1, "你好。", true) // final well inside the timeout
	h.waitState(StateListening)
	time.Sleep(300 * time.Millisecond) // past the timeout: must not fire
	if n := h.llm.count(); n != 1 {
		t.Errorf("LLM called %d times, want 1", n)
	}
}

func TestRetractedEarlyTurnDoesNotDuplicateWords(t *testing.T) {
	h := newPartialHarness(t, "BLOCK", "BLOCK", "两个都推荐。")
	h.llm.started = make(chan struct{}, 4)
	h.sayID(5, "介绍一下北京", false)
	h.waitFor("early turn", func(e Event) bool { return e.Type == "turn" })
	<-h.llm.started
	h.sayID(6, "还有", false) // a new sentence: the user is not done
	h.waitFor("retracted", func(e Event) bool { return e.Type == "retracted" })
	h.sayID(5, "介绍一下北京。", true) // the recognizer finally closes sentence 5
	h.sayID(6, "还有上海。", true)
	last := h.waitFor("merged turn", func(e Event) bool { return e.Type == "turn" && strings.Contains(e.Text, "上海") })
	if last.Text != "介绍一下北京。还有上海。" {
		t.Errorf("merged turn = %q, want each sentence once", last.Text)
	}
}

// waitGenerators fails if reply goroutines outlive Run.
func (h *harness) waitGenerators() {
	h.t.Helper()
	done := make(chan struct{})
	go func() { h.eng.gen.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		h.t.Fatal("reply goroutines leaked after Run returned")
	}
}

func TestCancelledTurnsDoNotLeakGoroutines(t *testing.T) {
	replies := make([]string, 0, 30)
	for i := 0; i < 15; i++ {
		replies = append(replies, "LEAK", "好的。")
	}
	h := newHarness(t, 30*time.Millisecond, replies...)
	for i := 0; i < 15; i++ {
		h.say("讲点什么", true)
		h.waitFor("first segment", func(e Event) bool { return e.Type == "assistant" })
		h.say("停一下", true) // barge-in, then a short reply
		h.waitFor("interrupted", func(e Event) bool { return e.Type == "interrupted" })
		h.waitState(StateListening)
	}
	h.stop()
	h.waitGenerators()
}

func TestTurnTimeoutWhileSpeakingEndsTurn(t *testing.T) {
	h := newHarnessTTS(t, func(c *Config) { c.TurnTimeout = 400 * time.Millisecond },
		fakeTTS{perRune: 100 * time.Millisecond, silentCancel: true, realTime: true},
		"这是一段很长很长很长很长很长很长很长的回答，要说好几秒钟才能说完。")
	h.say("说吧", true)
	h.waitState(StateSpeaking)
	h.waitState(StateListening) // must not stay "speaking" forever
	hist := texts(h.stop())
	if len(hist) != 2 || hist[0] != "user:说吧" || !strings.HasPrefix(hist[1], "assistant:") {
		t.Errorf("history = %q, want the heard part committed", hist)
	}
	h.waitGenerators()
}

func TestUserExtendingEarlySentenceInterrupts(t *testing.T) {
	h := newHarnessTTS(t, func(c *Config) { c.PartialEndpointTimeout = 150 * time.Millisecond },
		fakeTTS{perRune: 50 * time.Millisecond},
		"好的，我这就帮你订明天去上海的票，请稍等一下我查一查。", "酒店也一起订好了。")
	h.sayID(5, "帮我订明天去上海的票", false)
	h.waitFor("early turn", func(e Event) bool { return e.Type == "turn" })
	h.waitState(StateSpeaking)
	// Same sentence, new words: the user is still talking.
	h.sayID(5, "帮我订明天去上海的票再订个酒店", false)
	h.waitFor("interrupted", func(e Event) bool { return e.Type == "interrupted" })
	h.sayID(5, "帮我订明天去上海的票，再订个酒店。", true)
	next := h.waitFor("new turn", func(e Event) bool { return e.Type == "turn" })
	if !strings.Contains(next.Text, "酒店") {
		t.Errorf("new turn = %q, want the added request answered", next.Text)
	}
	h.waitState(StateListening)
}

// ---- soft barge-in ----

// loudSpeech is a 16 kHz frame well above any noise floor.
func loudSpeech() []int16 {
	f := make([]int16, 320)
	for i := range f {
		f[i] = int16(8000 * math.Sin(2*math.Pi*220*float64(i)/16000))
	}
	return f
}

// talk feeds d of loud audio through PushAudio in real time.
func (h *harness) talk(d time.Duration) {
	for i := 0; i < int(d/(20*time.Millisecond)); i++ {
		h.eng.PushAudio(context.Background(), loudSpeech())
		time.Sleep(20 * time.Millisecond)
	}
}

func (h *harness) quiet(d time.Duration) {
	for i := 0; i < int(d/(20*time.Millisecond)); i++ {
		h.eng.PushAudio(context.Background(), make([]int16, 320))
		time.Sleep(20 * time.Millisecond)
	}
}

func newSoftHarness(t *testing.T, replies ...string) *harness {
	t.Helper()
	return newHarnessWith(t, func(c *Config) { c.DuckHold = 400 * time.Millisecond }, 50*time.Millisecond, replies...)
}

const longReply = "这是一个很长很长的回答，会说上好一阵子，足够你在中间插话，我们来看看会发生什么事情。"

func TestVoiceOnsetDucksBeforeAnyText(t *testing.T) {
	h := newSoftHarness(t, longReply)
	h.quiet(200 * time.Millisecond) // establish the noise floor
	h.say("说吧", true)
	h.waitState(StateSpeaking)
	start := time.Now()
	go h.talk(600 * time.Millisecond)
	h.waitFor("yield", func(e Event) bool { return e.Type == "yield" })
	if d := time.Since(start); d > 400*time.Millisecond {
		t.Errorf("yielded %v after the user started, want ≤ 400ms", d)
	}
	if h.player.gainTarget() >= 1 {
		t.Error("player not ducked")
	}
}

func TestUnconfirmedYieldResumes(t *testing.T) {
	h := newSoftHarness(t, longReply)
	h.quiet(200 * time.Millisecond)
	h.say("说吧", true)
	h.waitState(StateSpeaking)
	h.talk(300 * time.Millisecond) // a cough: loud, but no words follow
	h.waitFor("yield", func(e Event) bool { return e.Type == "yield" })
	go h.quiet(2 * time.Second)
	h.waitFor("resume", func(e Event) bool { return e.Type == "resume" })
	if h.player.gainTarget() != 1 {
		t.Error("gain not restored")
	}
	h.waitState(StateListening) // the reply finished normally
	if hist := texts(h.stop()); len(hist) != 2 || strings.Contains(hist[1], "打断") {
		t.Errorf("history = %q, want an uninterrupted reply", hist)
	}
}

func TestBackchannelWhileYieldingResumes(t *testing.T) {
	h := newSoftHarness(t, longReply)
	h.quiet(200 * time.Millisecond)
	h.say("说吧", true)
	h.waitState(StateSpeaking)
	h.talk(300 * time.Millisecond)
	h.waitFor("yield", func(e Event) bool { return e.Type == "yield" })
	h.say("嗯嗯。", true)
	h.waitFor("resume", func(e Event) bool { return e.Type == "resume" })
}

func TestOneWordConfirmsWhenVoiceAgrees(t *testing.T) {
	h := newSoftHarness(t, longReply, "好的我停下。")
	h.quiet(200 * time.Millisecond)
	h.say("说吧", true)
	h.waitState(StateSpeaking)
	go h.talk(800 * time.Millisecond)
	h.waitFor("yield", func(e Event) bool { return e.Type == "yield" })
	time.Sleep(350 * time.Millisecond) // sustained voice
	h.say("停", false)                  // a single substantive character
	h.waitFor("interrupted", func(e Event) bool { return e.Type == "interrupted" })
}

func TestOneWordWithoutVoiceDoesNotInterrupt(t *testing.T) {
	h := newSoftHarness(t, longReply)
	h.say("说吧", true)
	h.waitState(StateSpeaking)
	h.say("停", false) // no acoustic evidence: stays below the threshold
	h.waitState(StateListening)
	if hist := texts(h.stop()); strings.Contains(hist[len(hist)-1], "打断") {
		t.Errorf("interrupted on one word without voice: %q", hist)
	}
}

func TestNaturalEndWhileDuckedRestoresGain(t *testing.T) {
	// ~0.65s of reply: long enough for voice to be detected mid-reply,
	// short enough to end while still ducked (DuckHold is 5s).
	h := newHarnessWith(t, func(c *Config) { c.DuckHold = 5 * time.Second }, 80*time.Millisecond, "这句话不算很长。")
	h.quiet(200 * time.Millisecond)
	h.say("说吧", true)
	h.waitState(StateSpeaking)
	go h.talk(200 * time.Millisecond)
	h.waitFor("yield", func(e Event) bool { return e.Type == "yield" })
	h.waitState(StateListening)
	if h.player.gainTarget() != 1 {
		t.Error("gain left ducked after the reply ended")
	}
}

func TestCoughThenOneWordDoesNotInterrupt(t *testing.T) {
	h := newSoftHarness(t, longReply)
	h.quiet(200 * time.Millisecond)
	h.say("说吧", true)
	h.waitState(StateSpeaking)
	h.talk(200 * time.Millisecond) // a cough
	h.waitFor("yield", func(e Event) bool { return e.Type == "yield" })
	h.quiet(150 * time.Millisecond) // quiet, inside the detector's hangover
	h.say("停", false)               // one character, maybe misheard echo
	h.waitState(StateListening)
	if hist := texts(h.stop()); strings.Contains(hist[len(hist)-1], "打断") {
		t.Errorf("a cough plus one character interrupted: %q", hist)
	}
}
