package asr

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Caxson/TalkTalk/internal/dashscope"
	"github.com/Caxson/TalkTalk/internal/dashscope/dashscopetest"
)

func newRec(t *testing.T, url string) *DashScope {
	t.Helper()
	r, err := NewDashScope(DashScopeConfig{
		Conn:                 dashscope.Config{URL: url, APIKey: "k"},
		Model:                "paraformer-realtime-v2",
		MaxSentenceSilenceMs: 500,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func sentence(id int, text string, end bool, begin int, endMs any) map[string]any {
	return map[string]any{"sentence": map[string]any{
		"sentence_id": id, "text": text, "sentence_end": end,
		"begin_time": begin, "end_time": endMs, "heartbeat": false,
	}}
}

func collect(t *testing.T, s Stream) []Result {
	t.Helper()
	var out []Result
	timeout := time.After(3 * time.Second)
	for {
		select {
		case r, ok := <-s.Results():
			if !ok {
				return out
			}
			out = append(out, r)
		case <-timeout:
			t.Fatal("results not closed")
		}
	}
}

func TestStreamFramesAudioAndMapsResults(t *testing.T) {
	var mu sync.Mutex
	var frames []int
	var params map[string]any
	srv := dashscopetest.New(t, dashscopetest.Handlers{
		Run: func(tk *dashscopetest.Task) {
			mu.Lock()
			params = tk.Params
			mu.Unlock()
			tk.Started()
			// Noise the client must filter.
			tk.Result(map[string]any{"sentence": map[string]any{"sentence_id": 0, "heartbeat": true}})
			tk.Result(sentence(1, "", true, 0, 780))
		},
		Audio: func(tk *dashscopetest.Task, pcm []byte) {
			mu.Lock()
			frames = append(frames, len(pcm))
			n := len(frames)
			mu.Unlock()
			if n == 2 {
				tk.Result(sentence(2, "你好", false, 100, nil))
				tk.Result(sentence(2, "你好世界。", true, 100, 900))
			}
		},
	})
	r := newRec(t, srv.URL)
	s, err := r.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// 20ms writes, like the WebRTC leg: 12 writes = 240ms = 2 full frames + 40ms.
	for i := 0; i < 12; i++ {
		if err := s.Write(context.Background(), make([]int16, 320)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	got := collect(t, s)
	if s.Err() != nil {
		t.Errorf("Err after clean close = %v", s.Err())
	}

	want := []Result{
		{SentenceID: 2, Text: "你好", BeginMs: 100},
		{SentenceID: 2, Text: "你好世界。", Final: true, BeginMs: 100, EndMs: 900},
	}
	if len(got) != len(want) {
		t.Fatalf("results = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("result %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	mu.Lock()
	defer mu.Unlock()
	// Two 100ms frames, then the 40ms tail flushed on Close.
	if len(frames) != 3 || frames[0] != 3200 || frames[1] != 3200 || frames[2] != 1280 {
		t.Errorf("frame sizes = %v, want [3200 3200 1280]", frames)
	}
	if params["format"] != "pcm" || params["sample_rate"] != float64(16000) ||
		params["max_sentence_silence"] != float64(500) || params["semantic_punctuation_enabled"] != false {
		t.Errorf("params = %v", params)
	}
}

func TestTaskFailureEndsStreamWithError(t *testing.T) {
	srv := dashscopetest.New(t, dashscopetest.Handlers{
		Audio: func(tk *dashscopetest.Task, pcm []byte) {
			tk.Failed("CLIENT_ERROR", "request timeout after 23 seconds.")
		},
	})
	s, err := newRec(t, srv.URL).Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s.Write(context.Background(), make([]int16, 1600))
	collect(t, s)
	var te *dashscope.TaskError
	if !errors.As(s.Err(), &te) || te.Code != "CLIENT_ERROR" {
		t.Errorf("Err = %v", s.Err())
	}
	if err := s.Write(context.Background(), make([]int16, 1600)); err == nil {
		t.Error("Write after failure succeeded")
	}
	s.Close()
}

func TestOpenFailsWhenTaskRejected(t *testing.T) {
	srv := dashscopetest.New(t, dashscopetest.Handlers{
		Run: func(tk *dashscopetest.Task) { tk.Failed("ModelNotFound", "no such model") },
	})
	if _, err := newRec(t, srv.URL).Open(context.Background()); err == nil {
		t.Error("Open succeeded on rejected task")
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	srv := dashscopetest.New(t, dashscopetest.Handlers{})
	s, err := newRec(t, srv.URL).Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("second Close = %v", err)
	}
}

func TestNewDashScopeValidates(t *testing.T) {
	for _, c := range []DashScopeConfig{
		{Model: "m"},
		{Conn: dashscope.Config{APIKey: "k"}},
		{Conn: dashscope.Config{APIKey: "k"}, Model: "m", MaxSentenceSilenceMs: 100},
	} {
		if _, err := NewDashScope(c); err == nil {
			t.Errorf("NewDashScope(%+v) succeeded", c)
		}
	}
}
