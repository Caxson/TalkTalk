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
	"github.com/Caxson/TalkTalk/internal/lipsync"
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

// delayedPlayer makes the next harness use a player that starts audio
// later than it is queued. Tests that set it must not run in parallel.
var delayedPlayer bool

func newHarnessTTS(t *testing.T, tune func(*Config), synth fakeTTS, replies ...string) *harness {
	t.Helper()
	player, err := playout.New(16000, func([]byte, time.Duration) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if delayedPlayer {
		// A rendered-video lookahead: audio plays 0.5 s after it is queued.
		player.SetLookahead(25, func([]int16) {}, func([]byte, time.Duration) error { return nil })
	}
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

func TestOneWordConfirmsWhenVoiceAndCameraAgree(t *testing.T) {
	h := newSoftHarness(t, longReply, "好的我停下。")
	h.quiet(200 * time.Millisecond)
	h.say("说吧", true)
	h.waitState(StateSpeaking)
	defer h.holdVisual(func() Visual { return Visual{FacePresent: true, MouthActive: true} })()
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

// ---- lip-sync motion ----

// fakeLips emits one frame per 1/30 s of pushed audio, with jawOpen set to
// the segment's frame count so order is checkable; fail makes Push error.
type fakeLips struct {
	rate, pushed, emitted int
	fail                  bool
}

func (f *fakeLips) Push(pcm []int16) ([]lipsync.Frame, error) {
	if f.fail {
		return nil, errors.New("model exploded")
	}
	f.pushed += len(pcm)
	return f.take(f.pushed * lipsync.FrameRate / f.rate), nil
}

func (f *fakeLips) Flush() ([]lipsync.Frame, error) {
	return f.take((f.pushed*lipsync.FrameRate + f.rate - 1) / f.rate), nil
}

func (f *fakeLips) take(upto int) []lipsync.Frame {
	var out []lipsync.Frame
	for ; f.emitted < upto; f.emitted++ {
		var fr lipsync.Frame
		fr[24] = float32(f.emitted%250) / 255
		out = append(out, fr)
	}
	return out
}

func newLipHarness(t *testing.T, fail bool, replies ...string) (*harness, *motionLog) {
	t.Helper()
	log := &motionLog{}
	h := newHarnessTTS(t, nil, fakeTTS{perRune: 60 * time.Millisecond, realTime: true}, replies...)
	h.player.SetMotionSink(log.add)
	h.eng.d.Lips = func(rate int) (LipStream, error) { return &fakeLips{rate: rate, fail: fail}, nil }
	return h, log
}

type motionLog struct {
	mu    sync.Mutex
	count int
	jaws  []byte
}

func (m *motionLog) add(b []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.count++
	m.jaws = append(m.jaws, b[MotionHeader+24])
}

func (m *motionLog) snapshot() (int, []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.count, append([]byte(nil), m.jaws...)
}

func TestMotionFramesFollowAudio(t *testing.T) {
	reply := "第一句话说完了。第二句也很快说完。"
	h, log := newLipHarness(t, false, reply)
	h.say("说话", true)
	h.waitState(StateSpeaking)
	h.waitState(StateListening)
	n, jaws := log.snapshot()
	// 60 ms of audio per rune, 30 frames per second, per segment.
	want := 0
	for _, seg := range []string{"第一句话说完了。", "第二句也很快说完。"} {
		want += (len([]rune(seg))*60*lipsync.FrameRate + 999) / 1000
	}
	if n < want-2 || n > want+2 {
		t.Errorf("motion frames sent = %d, want ≈ %d", n, want)
	}
	// Within a segment, frames arrive in order.
	for i := 1; i < len(jaws) && i < 10; i++ {
		if jaws[i] < jaws[i-1] {
			t.Fatalf("motion out of order at %d: %v", i, jaws[:10])
		}
	}
}

func TestIdlePlayerHoldsLeadAudio(t *testing.T) {
	h, _ := newLipHarness(t, false, "这一句话足够长，能让我们看到开头被稍微压后了一点点。")
	h.say("说话", true)
	m := h.waitFor("metrics", func(e Event) bool { return e.Type == "metrics" })
	// realTime TTS: 500 ms of audio takes ≈500 ms to arrive.
	if lead := m.Metrics.FirstAudioOut - m.Metrics.TTSFirstAudio; lead < 400*time.Millisecond {
		t.Errorf("first audio only %v after TTS started; lip-sync lead not held", lead)
	}
	h.waitState(StateListening)
}

func TestLipFailureKeepsTalking(t *testing.T) {
	h, log := newLipHarness(t, true, "口型坏了也要把话说完。")
	h.say("说话", true)
	h.waitState(StateSpeaking)
	h.waitState(StateListening)
	if n, _ := log.snapshot(); n != 0 {
		t.Errorf("motion sent despite failing lip-sync: %d", n)
	}
	if hist := texts(h.stop()); len(hist) != 2 || hist[1] != "assistant:口型坏了也要把话说完。" {
		t.Errorf("history = %q", hist)
	}
}

// ---- visual echo veto ----

func TestClosedMouthOnCameraVetoesYieldAndBargeIn(t *testing.T) {
	h := newSoftHarness(t, longReply)
	h.quiet(200 * time.Millisecond)
	h.say("说吧", true)
	h.waitState(StateSpeaking)

	// The user is on camera with a closed mouth: what the mic hears is echo.
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			h.eng.SetVisual(Visual{FacePresent: true, MouthActive: false})
			time.Sleep(50 * time.Millisecond)
		}
	}()
	defer close(stop)
	h.talk(500 * time.Millisecond) // loud "voice" = residual echo
	h.say("我是谁", false)            // echo transcribed as words
	h.waitState(StateListening)    // the reply ran to its natural end
	hist := texts(h.stop())
	if strings.Contains(hist[len(hist)-1], "打断") {
		t.Errorf("echo interrupted the reply: %q", hist)
	}
}

func TestSpeakingOnCameraStillInterrupts(t *testing.T) {
	h := newSoftHarness(t, longReply, "好。")
	h.quiet(200 * time.Millisecond)
	h.say("说吧", true)
	h.waitState(StateSpeaking)
	go func() {
		for i := 0; i < 30; i++ {
			h.eng.SetVisual(Visual{FacePresent: true, MouthActive: true})
			time.Sleep(50 * time.Millisecond)
		}
	}()
	go h.talk(600 * time.Millisecond)
	h.waitFor("yield", func(e Event) bool { return e.Type == "yield" })
	h.say("等一下", false)
	h.waitFor("interrupted", func(e Event) bool { return e.Type == "interrupted" })
}

func TestStaleOrAbsentVisualDoesNotVeto(t *testing.T) {
	h := newSoftHarness(t, longReply, "好。")
	h.quiet(200 * time.Millisecond)
	h.say("说吧", true)
	h.waitState(StateSpeaking)
	// A closed-mouth reading that is too old to trust, and one with
	// nobody on camera: neither may block a real interruption.
	h.eng.SetVisual(Visual{FacePresent: true, MouthActive: false})
	time.Sleep(700 * time.Millisecond)
	h.eng.SetVisual(Visual{FacePresent: false})
	go h.talk(600 * time.Millisecond)
	h.waitFor("yield", func(e Event) bool { return e.Type == "yield" })
	h.say("等一下", false)
	h.waitFor("interrupted", func(e Event) bool { return e.Type == "interrupted" })
}

// holdVisual keeps sending a camera reading until the returned func is called.
func (h *harness) holdVisual(v func() Visual) (stop func()) {
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			default:
			}
			h.eng.SetVisual(v())
			time.Sleep(50 * time.Millisecond)
		}
	}()
	return func() { close(done) }
}

func TestLongSpeechInterruptsDespiteStillMouthReading(t *testing.T) {
	// Profile, a mask or a frozen camera can read "mouth still" while the
	// user really talks: a long utterance must still get through.
	h := newSoftHarness(t, longReply, "好的。")
	h.quiet(200 * time.Millisecond)
	h.say("说吧", true)
	h.waitState(StateSpeaking)
	defer h.holdVisual(func() Visual { return Visual{FacePresent: true, MouthActive: false} })()
	h.talk(1200 * time.Millisecond)
	h.say("我不想听这个了我们换个话题聊聊周末去哪", false)
	h.waitFor("interrupted", func(e Event) bool { return e.Type == "interrupted" })
}

func TestVetoedOnsetYieldsOnceTheMouthIsSeenMoving(t *testing.T) {
	h := newSoftHarness(t, longReply)
	h.quiet(200 * time.Millisecond)
	h.say("说吧", true)
	h.waitState(StateSpeaking)
	var mu sync.Mutex
	moving := false
	defer h.holdVisual(func() Visual { mu.Lock(); defer mu.Unlock(); return Visual{FacePresent: true, MouthActive: moving} })()
	time.Sleep(120 * time.Millisecond)
	go h.talk(900 * time.Millisecond)
	time.Sleep(250 * time.Millisecond) // the camera catches up after the sound
	mu.Lock()
	moving = true
	mu.Unlock()
	h.waitFor("yield", func(e Event) bool { return e.Type == "yield" })
}

func TestOwnWordsHeardBackAreEchoWithoutACamera(t *testing.T) {
	h := newSoftHarness(t, longReply)
	h.say("说吧", true)
	h.waitState(StateSpeaking)
	h.waitFor("segment", func(e Event) bool { return e.Type == "assistant" })
	// The recognizer transcribes the assistant's own voice from the speakers,
	// following it as it plays.
	h.say("这是一个很长很长的回答", false)
	h.waitFor("second segment", func(e Event) bool { return e.Type == "assistant" })
	h.say("这是一个很长很长的回答，会说上好一阵子", true)
	h.waitState(StateListening)
	if hist := texts(h.stop()); strings.Contains(hist[len(hist)-1], "打断") || len(hist) != 2 {
		t.Errorf("own words heard back interrupted the reply: %q", hist)
	}
}

func TestEchoSimilarity(t *testing.T) {
	const said = "好的，这是一个很长的回答，会说上一阵子。"
	cases := []struct {
		heard, recent string
		echo          bool
	}{
		{"这是一个很长的回答", said, true},
		{"等一下我想换个话题", said, false},
		{"嗨，你", "嗨你好呀。", true},    // a short fragment of what was just said
		{"呃你好。", "嗨你好呀。", true},   // fillers do not hide it
		{"等等", "嗨你好呀。", false},    // short, but not what was said
		{"回答", "完全不同的一句。", false}, // in the turn, but not recent
		{"你", "你好", false},        // one character judges nothing
		{"", said, false},
	}
	for _, c := range cases {
		if got := soundsLikeOwnWords(c.heard, c.recent, said); got != c.echo {
			t.Errorf("soundsLikeOwnWords(%q, recent %q) = %v, want %v", c.heard, c.recent, got, c.echo)
		}
	}
}

func TestOneWordWithSustainedSoundButNoCameraDoesNotInterrupt(t *testing.T) {
	// Residual echo is sustained sound: without the camera seeing a moving
	// mouth, one character is not enough.
	h := newSoftHarness(t, longReply)
	h.quiet(200 * time.Millisecond)
	h.say("说吧", true)
	h.waitState(StateSpeaking)
	go h.talk(800 * time.Millisecond)
	h.waitFor("yield", func(e Event) bool { return e.Type == "yield" })
	time.Sleep(350 * time.Millisecond)
	h.say("Hi", false)
	h.waitState(StateListening)
	if hist := texts(h.stop()); strings.Contains(hist[len(hist)-1], "打断") {
		t.Errorf("one word over sustained sound without a camera interrupted: %q", hist)
	}
}

func TestSubstantiveUnits(t *testing.T) {
	for text, want := range map[string]int{
		"Hi": 1, "Hi there": 2, "嗨": 1, "我是talk talk。": 4, "嗯嗯": 0, "停": 1, "3.5 度": 3, "": 0,
	} {
		if got := substantive(text); got != want {
			t.Errorf("substantive(%q) = %d, want %d", text, got, want)
		}
	}
}

func TestEchoOfLastSentenceAfterReplyEndsIsNotANewTurn(t *testing.T) {
	h := newHarness(t, time.Millisecond, "你好呀。你想说什么呢？", "不该出现。")
	h.say("你好", true)
	h.waitState(StateSpeaking)
	h.waitState(StateListening)
	// The recognizer's final for the last sentence arrives after the reply
	// has finished playing.
	h.say("想说什么呢", false)
	h.say("想说什么呢？", true)
	time.Sleep(200 * time.Millisecond)
	if n := h.llm.count(); n != 1 {
		t.Errorf("the assistant answered its own echo: %d LLM calls", n)
	}
	// A real answer right after is still heard.
	h.say("我想聊聊旅行", true)
	h.waitFor("turn", func(e Event) bool { return e.Type == "turn" && e.Text == "我想聊聊旅行" })
}

func TestEchoTailExpires(t *testing.T) {
	h := newHarnessWith(t, func(c *Config) { c.EchoTail = 150 * time.Millisecond }, time.Millisecond, "你好呀。", "好的。")
	h.say("你好", true)
	h.waitState(StateSpeaking)
	h.waitState(StateListening)
	time.Sleep(300 * time.Millisecond)
	h.say("你好呀", true) // long after: the user may well say it
	h.waitFor("turn", func(e Event) bool { return e.Type == "turn" && e.Text == "你好呀" })
}
