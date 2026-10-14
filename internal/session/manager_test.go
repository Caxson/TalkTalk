package session

import (
	"context"
	"errors"
	"io"
	"iter"
	"log/slog"
	"math"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/Caxson/TalkTalk/internal/asr"
	"github.com/Caxson/TalkTalk/internal/dialog"
	"github.com/Caxson/TalkTalk/internal/llm"
	"github.com/Caxson/TalkTalk/internal/photoreal/photorealtest"
	"github.com/Caxson/TalkTalk/internal/rtc/rtctest"
)

// heardRecognizer emits one final transcript once enough audio arrived,
// proving the uplink reaches recognition.
type heardRecognizer struct{}

func (heardRecognizer) SampleRate() int { return 16000 }
func (heardRecognizer) Open(context.Context) (asr.Stream, error) {
	return &heardStream{results: make(chan asr.Result, 4)}, nil
}

type heardStream struct {
	mu      sync.Mutex
	samples int
	sent    bool
	results chan asr.Result
	closed  bool
}

func (s *heardStream) Write(_ context.Context, pcm []int16) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.samples += len(pcm)
	if !s.sent && s.samples >= 8000 && !s.closed {
		s.sent = true
		s.results <- asr.Result{SentenceID: 1, Text: "你好", Final: true, EndMs: 500}
	}
	return nil
}
func (s *heardStream) Results() <-chan asr.Result { return s.results }
func (s *heardStream) Err() error                 { return nil }
func (s *heardStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.results)
	}
	return nil
}

type replyLLM struct{}

func (replyLLM) Stream(context.Context, []llm.Message) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) { yield("你好呀，很高兴见到你。", nil) }
}

type toneSynth struct {
	mu     sync.Mutex
	closed bool
}

func (*toneSynth) SampleRate() int            { return 24000 }
func (*toneSynth) Warm(context.Context) error { return nil }
func (s *toneSynth) Close() error             { s.mu.Lock(); s.closed = true; s.mu.Unlock(); return nil }
func (*toneSynth) Synthesize(ctx context.Context, text string) iter.Seq2[[]int16, error] {
	return func(yield func([]int16, error) bool) {
		pcm := make([]int16, 24000/2) // 0.5s per segment
		for i := range pcm {
			pcm[i] = int16(6000 * math.Sin(2*math.Pi*300*float64(i)/24000))
		}
		yield(pcm, nil)
	}
}

func newManager(t *testing.T, max int) (*Manager, *[]*toneSynth) {
	t.Helper()
	var mu sync.Mutex
	synths := &[]*toneSynth{}
	m, err := NewManager(Deps{
		Recognizer: heardRecognizer{},
		LLM:        replyLLM{},
		NewSynth: func(string) (Synth, error) {
			s := &toneSynth{}
			mu.Lock()
			*synths = append(*synths, s)
			mu.Unlock()
			return s, nil
		},
		Dialog: dialog.Config{SystemPrompt: "SYS"},
		Max:    max,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		m.CloseAll(ctx)
	})
	return m, synths
}

func waitEvent(t *testing.T, c *rtctest.Client, pred func(map[string]any) bool) {
	t.Helper()
	timeout := time.After(8 * time.Second)
	for {
		select {
		case ev := <-c.Events:
			if pred(ev) {
				return
			}
		case <-timeout:
			t.Fatal("expected event did not arrive")
		}
	}
}

func TestSessionRunsAConversationOverWebRTC(t *testing.T) {
	m, synths := newManager(t, 2)
	c := rtctest.New(t)
	s, answer, err := m.Create(context.Background(), c.Offer(t), "")
	if err != nil {
		t.Fatal(err)
	}
	c.Accept(t, answer)
	<-c.DCOpened

	go c.Speak(make([]int16, 48000)) // 1s of microphone audio
	waitEvent(t, c, func(e map[string]any) bool { return e["type"] == "user" && e["text"] == "你好" })
	waitEvent(t, c, func(e map[string]any) bool { return e["type"] == "state" && e["state"] == "speaking" })
	waitEvent(t, c, func(e map[string]any) bool { return e["type"] == "metrics" })
	waitEvent(t, c, func(e map[string]any) bool { return e["type"] == "state" && e["state"] == "listening" })
	if c.Packets() < 50 {
		t.Errorf("client got %d downlink packets, want ≥ 50", c.Packets())
	}

	if !m.Close(s.ID) {
		t.Fatal("Close returned false for a live session")
	}
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("session did not shut down")
	}
	if m.Count() != 0 {
		t.Errorf("Count = %d after close", m.Count())
	}
	if !(*synths)[0].closed {
		t.Error("synthesizer not closed with the session")
	}
}

func TestCapacityLimit(t *testing.T) {
	m, _ := newManager(t, 1)
	c1 := rtctest.New(t)
	if _, _, err := m.Create(context.Background(), c1.Offer(t), ""); err != nil {
		t.Fatal(err)
	}
	c2 := rtctest.New(t)
	if _, _, err := m.Create(context.Background(), c2.Offer(t), ""); !errors.Is(err, ErrCapacity) {
		t.Errorf("second Create err = %v, want ErrCapacity", err)
	}
}

func TestClientHangupEndsSession(t *testing.T) {
	m, _ := newManager(t, 1)
	c := rtctest.New(t)
	s, answer, err := m.Create(context.Background(), c.Offer(t), "")
	if err != nil {
		t.Fatal(err)
	}
	c.Accept(t, answer)
	<-c.DCOpened
	c.PC.Close()
	select {
	case <-s.Done():
	case <-time.After(15 * time.Second):
		t.Fatal("session survived client hangup")
	}
	if m.Count() != 0 {
		t.Errorf("Count = %d", m.Count())
	}
}

func TestBadOfferReleasesSlot(t *testing.T) {
	m, _ := newManager(t, 1)
	c := rtctest.New(t)
	offer := c.Offer(t)
	offer.SDP = "garbage"
	if _, _, err := m.Create(context.Background(), offer, ""); err == nil {
		t.Fatal("Create accepted garbage SDP")
	}
	if m.Count() != 0 {
		t.Errorf("slot leaked: Count = %d", m.Count())
	}
	if m.Close("nope") {
		t.Error("Close of unknown id returned true")
	}
}

func TestCloseAllRefusesNewSessions(t *testing.T) {
	m, _ := newManager(t, 2)
	m.CloseAll(context.Background())
	c := rtctest.New(t)
	if _, _, err := m.Create(context.Background(), c.Offer(t), ""); err == nil {
		t.Error("Create succeeded after CloseAll")
	}
}

func TestTeardownBeforeRegistrationFreesSlot(t *testing.T) {
	m, _ := newManager(t, 1)
	c := rtctest.New(t)
	offer := c.Offer(t)
	c.PC.Close() // the client is gone before the answer is even applied
	s, _, err := m.Create(context.Background(), offer, "")
	if err != nil {
		t.Fatal(err)
	}
	m.Close(s.ID)
	select {
	case <-s.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("session did not shut down")
	}
	if m.Count() != 0 {
		t.Fatalf("slot leaked: Count = %d", m.Count())
	}
	c2 := rtctest.New(t)
	if _, _, err := m.Create(context.Background(), c2.Offer(t), ""); err != nil {
		t.Errorf("capacity not restored: %v", err)
	}
}

func TestReleaseIgnoresOtherOwner(t *testing.T) {
	m, _ := newManager(t, 2)
	other := &Session{ID: "x"}
	m.mu.Lock()
	m.sessions["x"] = other
	m.mu.Unlock()
	m.release("x", &Session{ID: "x"}) // a stale teardown must not evict
	if m.Count() != 1 {
		t.Errorf("Count = %d, want 1", m.Count())
	}
	m.release("x", other)
	if m.Count() != 0 {
		t.Errorf("Count = %d, want 0", m.Count())
	}
}

// promptLLM records the system prompt it was given.
type promptLLM struct{ got chan string }

func (l promptLLM) Stream(_ context.Context, msgs []llm.Message) iter.Seq2[string, error] {
	if len(msgs) > 0 && msgs[0].Role == llm.RoleSystem {
		select {
		case l.got <- msgs[0].Content:
		default:
		}
	}
	return replyLLM{}.Stream(context.Background(), msgs)
}

func TestSessionUsesTheChosenCharacter(t *testing.T) {
	voices := make(chan string, 4)
	prompts := promptLLM{got: make(chan string, 4)}
	m, err := NewManager(Deps{
		Recognizer: heardRecognizer{},
		LLM:        prompts,
		NewSynth: func(voice string) (Synth, error) {
			voices <- voice
			return &toneSynth{}, nil
		},
		Dialog: dialog.Config{SystemPrompt: "SYS"},
		Persona: func(id string) (Persona, error) {
			switch id {
			case "", "mio":
				return Persona{Voice: "v-mio", SystemPrompt: "SYS mio"}, nil
			}
			return Persona{}, ErrUnknownCharacter
		},
		Max:    1,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		m.CloseAll(ctx)
	})

	if _, _, err := m.Create(context.Background(), rtctest.New(t).Offer(t), "ghost"); !errors.Is(err, ErrUnknownCharacter) {
		t.Fatalf("unknown character: err = %v", err)
	}
	if m.Count() != 0 {
		t.Fatalf("unknown character held a slot: Count = %d", m.Count())
	}

	c := rtctest.New(t)
	_, answer, err := m.Create(context.Background(), c.Offer(t), "mio")
	if err != nil {
		t.Fatal(err)
	}
	if v := <-voices; v != "v-mio" {
		t.Errorf("synth voice = %q, want v-mio", v)
	}
	c.Accept(t, answer)
	<-c.DCOpened
	go c.Speak(make([]int16, 48000))
	select {
	case p := <-prompts.got:
		if p != "SYS mio" {
			t.Errorf("system prompt = %q, want the character's", p)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("LLM never called")
	}
}

func TestNoCastRejectsNamedCharacters(t *testing.T) {
	m, _ := newManager(t, 1)
	c := rtctest.New(t)
	if _, _, err := m.Create(context.Background(), c.Offer(t), "mio"); !errors.Is(err, ErrUnknownCharacter) {
		t.Errorf("err = %v, want ErrUnknownCharacter", err)
	}
}

func newVideoManager(t *testing.T, addr string) *Manager {
	t.Helper()
	m, err := NewManager(Deps{
		Recognizer: heardRecognizer{},
		LLM:        replyLLM{},
		NewSynth:   func(string) (Synth, error) { return &toneSynth{}, nil },
		Dialog:     dialog.Config{SystemPrompt: "SYS"},
		Persona: func(id string) (Persona, error) {
			return Persona{Character: "rin", Video: "rin", SystemPrompt: "SYS"}, nil
		},
		Video:  VideoDeps{Addr: addr, Lead: 200 * time.Millisecond},
		Max:    1,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		m.CloseAll(ctx)
	})
	return m
}

func TestPhotorealSessionSendsVideoWithVoice(t *testing.T) {
	w := photorealtest.New(t)
	m := newVideoManager(t, w.Addr)
	c := rtctest.New(t)
	_, answer, err := m.Create(context.Background(), c.Offer(t), "rin")
	if err != nil {
		t.Fatal(err)
	}
	c.Accept(t, answer)
	<-c.DCOpened
	go c.Speak(make([]int16, 48000))
	waitEvent(t, c, func(e map[string]any) bool {
		return e["type"] == "state" && e["state"] == "listening" && c.Packets() > 0
	})
	deadline := time.Now().Add(5 * time.Second)
	for c.VideoPackets() < 10 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if c.VideoPackets() < 10 {
		t.Errorf("client got %d video packets", c.VideoPackets())
	}
	if got := w.Characters(); len(got) != 1 || got[0] != "rin" {
		t.Errorf("worker asked for %v", got)
	}
	if w.Samples() == 0 {
		t.Error("worker got no audio")
	}
}

func TestPhotorealUnavailableFallsBackToVoice(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close() // nothing listens: the worker is down
	m := newVideoManager(t, addr)
	c := rtctest.New(t)
	_, answer, err := m.Create(context.Background(), c.Offer(t), "rin")
	if err != nil {
		t.Fatalf("session failed without the worker: %v", err)
	}
	c.Accept(t, answer)
	<-c.DCOpened
	go c.Speak(make([]int16, 48000))
	waitEvent(t, c, func(e map[string]any) bool { return e["type"] == "state" && e["state"] == "speaking" })
	if c.VideoPackets() != 0 {
		t.Errorf("video packets without a worker: %d", c.VideoPackets())
	}
}
