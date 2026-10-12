package tts

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Caxson/TalkTalk/internal/audio"
	"github.com/Caxson/TalkTalk/internal/dashscope"
)

// Run with: TALKTALK_IT=1 go test ./internal/tts -run Integration -v
func realSynth(t *testing.T) *DashScope {
	t.Helper()
	if os.Getenv("TALKTALK_IT") == "" {
		t.Skip("set TALKTALK_IT=1 to hit real providers")
	}
	key := os.Getenv("DASHSCOPE_API_KEY")
	if key == "" {
		t.Fatal("DASHSCOPE_API_KEY not set")
	}
	s, err := NewDashScope(DashScopeConfig{
		Conn:       dashscope.Config{APIKey: key},
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

func TestIntegrationCosyVoice(t *testing.T) {
	s := realSynth(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.Warm(ctx); err != nil {
		t.Fatal(err)
	}

	for i, text := range []string{"你好，我是 TalkTalk。", "今天想聊点什么？"} {
		start := time.Now()
		var first time.Duration
		var pcm []int16
		for chunk, err := range s.Synthesize(ctx, text) {
			if err != nil {
				t.Fatalf("segment %d: %v", i, err)
			}
			if first == 0 {
				first = time.Since(start)
			}
			pcm = append(pcm, chunk...)
		}
		dur := time.Duration(len(pcm)) * time.Second / 24000
		t.Logf("segment %d %q: first-audio=%v total=%v audio=%v level=%.1fdBFS",
			i, text, first, time.Since(start), dur, audio.RMSdBFS(pcm))
		if dur < 500*time.Millisecond || audio.RMSdBFS(pcm) < -40 {
			t.Errorf("segment %d: implausible audio (%v, %.1f dBFS)", i, dur, audio.RMSdBFS(pcm))
		}
	}

	// Cancel mid-stream, then the warm connection must still work.
	cctx, ccancel := context.WithCancel(ctx)
	got := 0
	for chunk, err := range s.Synthesize(cctx, "这是一段很长很长的话，用来测试在合成过程中途取消，看看服务端会不会正确地停下来，然后连接还能不能继续复用。") {
		if err != nil {
			t.Fatalf("long segment: %v", err)
		}
		got += len(chunk)
		ccancel()
	}
	ccancel()
	t.Logf("cancelled after %d samples", got)

	start := time.Now()
	var n int
	for chunk, err := range s.Synthesize(ctx, "取消之后还能说话。") {
		if err != nil {
			t.Fatalf("after cancel: %v", err)
		}
		n += len(chunk)
	}
	t.Logf("after cancel: %d samples in %v, conn reused=%v", n, time.Since(start), s.conn != nil && s.conn.Alive())
	if n == 0 {
		t.Error("no audio after cancel")
	}
}
