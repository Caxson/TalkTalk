package tts

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Caxson/TalkTalk/internal/audio"
	"github.com/Caxson/TalkTalk/internal/dashscope"
	"github.com/Caxson/TalkTalk/internal/dashscope/dashscopetest"
)

func newSynth(t *testing.T, url string) *DashScope {
	t.Helper()
	s, err := NewDashScope(DashScopeConfig{
		Conn:       dashscope.Config{URL: url, APIKey: "k"},
		Model:      "cosyvoice-v3-flash",
		Voice:      "longxiaochun_v3",
		SampleRate: 24000,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func synth(t *testing.T, s *DashScope, ctx context.Context, text string) ([]int16, error) {
	t.Helper()
	var out []int16
	for pcm, err := range s.Synthesize(ctx, text) {
		if err != nil {
			return out, err
		}
		out = append(out, pcm...)
	}
	return out, nil
}

// echoSamples replies to each text with len(text) samples valued 1..n,
// split at an odd byte boundary to exercise reassembly.
func echoHandlers(gotParams *sync.Map) dashscopetest.Handlers {
	return dashscopetest.Handlers{
		Run: func(tk *dashscopetest.Task) {
			if gotParams != nil {
				gotParams.Store("params", tk.Params)
				gotParams.Store("model", tk.Model)
			}
			tk.Started()
		},
		Text: func(tk *dashscopetest.Task, text string) {
			n := len([]rune(text))
			s := make([]int16, n)
			for i := range s {
				s[i] = int16(i + 1)
			}
			b := audio.Int16ToBytes(s)
			tk.Result(map[string]any{"type": "sentence-synthesis"})
			cut := min(3, len(b)-1) // odd split exercises byte reassembly
			tk.Audio(b[:cut])
			tk.Audio(b[cut:])
		},
	}
}

func TestSynthesizeReassemblesPCMAndPassesParams(t *testing.T) {
	var got sync.Map
	srv := dashscopetest.New(t, echoHandlers(&got))
	s := newSynth(t, srv.URL)

	pcm, err := synth(t, s, context.Background(), "你好世界")
	if err != nil {
		t.Fatal(err)
	}
	if len(pcm) != 4 || pcm[0] != 1 || pcm[3] != 4 {
		t.Errorf("pcm = %v, want [1 2 3 4]", pcm)
	}
	params, _ := got.Load("params")
	p := params.(map[string]any)
	if p["voice"] != "longxiaochun_v3" || p["format"] != "pcm" || p["sample_rate"] != float64(24000) {
		t.Errorf("params = %v", p)
	}
	if m, _ := got.Load("model"); m != "cosyvoice-v3-flash" {
		t.Errorf("model = %v", m)
	}
	if s.SampleRate() != 24000 {
		t.Errorf("SampleRate = %d", s.SampleRate())
	}
}

func TestConnectionIsReusedAcrossSegments(t *testing.T) {
	srv := dashscopetest.New(t, echoHandlers(nil))
	s := newSynth(t, srv.URL)
	for _, text := range []string{"一", "二三", "四五六"} {
		pcm, err := synth(t, s, context.Background(), text)
		if err != nil || len(pcm) != len([]rune(text)) {
			t.Fatalf("%q: pcm=%d err=%v", text, len(pcm), err)
		}
	}
	if srv.Conns() != 1 || srv.Tasks() != 3 {
		t.Errorf("conns=%d tasks=%d, want 1 and 3", srv.Conns(), srv.Tasks())
	}
}

func TestWarmPreDials(t *testing.T) {
	srv := dashscopetest.New(t, echoHandlers(nil))
	s := newSynth(t, srv.URL)
	if err := s.Warm(context.Background()); err != nil {
		t.Fatal(err)
	}
	if srv.Conns() != 1 {
		t.Errorf("conns after Warm = %d", srv.Conns())
	}
	synth(t, s, context.Background(), "好")
	if srv.Conns() != 1 {
		t.Errorf("Synthesize redialed after Warm: conns=%d", srv.Conns())
	}
}

func TestCancelSendsCancelDirectiveAndKeepsConnection(t *testing.T) {
	cancelled := make(chan bool, 1)
	srv := dashscopetest.New(t, dashscopetest.Handlers{
		Text: func(tk *dashscopetest.Task, text string) {
			tk.Audio(make([]byte, 320))
		},
		Finish: func(tk *dashscopetest.Task, cancel bool) {
			if cancel {
				cancelled <- true
			}
			tk.Finished()
		},
	})
	s := newSynth(t, srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, err := range s.Synthesize(ctx, "很长的一句话") {
		if err != nil {
			t.Fatal(err)
		}
		cancel() // after first chunk
	}
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("cancel directive not sent")
	}
	// Connection survives a clean cancel and is reused.
	if _, err := synth(t, s, context.Background(), "下一句"); err != nil {
		t.Fatal(err)
	}
	if srv.Conns() != 1 {
		t.Errorf("conns = %d, want reuse after cancel", srv.Conns())
	}
}

func TestBreakingEarlyAbortsTask(t *testing.T) {
	srv := dashscopetest.New(t, dashscopetest.Handlers{
		Text: func(tk *dashscopetest.Task, text string) {
			tk.Audio(make([]byte, 320))
			tk.Audio(make([]byte, 320))
		},
	})
	s := newSynth(t, srv.URL)
	for range s.Synthesize(context.Background(), "abc") {
		break
	}
	if _, err := synth(t, s, context.Background(), "next"); err != nil {
		t.Fatalf("synth after break: %v", err)
	}
}

func TestTaskFailureRedialsNextTime(t *testing.T) {
	var mu sync.Mutex
	failNext := true
	srv := dashscopetest.New(t, dashscopetest.Handlers{
		Text: func(tk *dashscopetest.Task, text string) {
			mu.Lock()
			fail := failNext
			failNext = false
			mu.Unlock()
			if fail {
				tk.Failed("InvalidParameter", "Engine return error code: 418")
				return
			}
			tk.Audio(make([]byte, 4))
		},
	})
	s := newSynth(t, srv.URL)
	_, err := synth(t, s, context.Background(), "x")
	var te *dashscope.TaskError
	if !errors.As(err, &te) || !strings.Contains(err.Error(), "418") {
		t.Fatalf("err = %v, want TaskError 418", err)
	}
	pcm, err := synth(t, s, context.Background(), "y")
	if err != nil || len(pcm) != 2 {
		t.Fatalf("retry: pcm=%v err=%v", pcm, err)
	}
	if srv.Conns() != 2 {
		t.Errorf("conns = %d, want redial after failure", srv.Conns())
	}
}

func TestEmptyTextYieldsNothing(t *testing.T) {
	srv := dashscopetest.New(t, echoHandlers(nil))
	s := newSynth(t, srv.URL)
	pcm, err := synth(t, s, context.Background(), "  \n ")
	if err != nil || len(pcm) != 0 || srv.Tasks() != 0 {
		t.Errorf("pcm=%v err=%v tasks=%d", pcm, err, srv.Tasks())
	}
}

func TestNewDashScopeValidates(t *testing.T) {
	bad := []DashScopeConfig{
		{Model: "m", Voice: "v", SampleRate: 24000},
		{Conn: dashscope.Config{APIKey: "k"}, Voice: "v", SampleRate: 24000},
		{Conn: dashscope.Config{APIKey: "k"}, Model: "m", SampleRate: 24000},
		{Conn: dashscope.Config{APIKey: "k"}, Model: "m", Voice: "v", SampleRate: 44100},
	}
	for _, c := range bad {
		if _, err := NewDashScope(c); err == nil {
			t.Errorf("NewDashScope(%+v) succeeded", c)
		}
	}
}
