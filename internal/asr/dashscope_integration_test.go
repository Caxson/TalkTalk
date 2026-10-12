package asr

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Caxson/TalkTalk/internal/dashscope"
	"github.com/Caxson/TalkTalk/internal/tts"
)

// Run with: TALKTALK_IT=1 go test ./internal/asr -run Integration -v
// Synthesizes real speech with CosyVoice, then streams it in real time.
func TestIntegrationRoundTrip(t *testing.T) {
	if os.Getenv("TALKTALK_IT") == "" {
		t.Skip("set TALKTALK_IT=1 to hit real providers")
	}
	key := os.Getenv("DASHSCOPE_API_KEY")
	conn := dashscope.Config{APIKey: key}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	synth, err := tts.NewDashScope(tts.DashScopeConfig{Conn: conn, Model: "cosyvoice-v3-flash", Voice: "longxiaochun_v3", SampleRate: 16000})
	if err != nil {
		t.Fatal(err)
	}
	defer synth.Close()
	const phrase = "明天上午帮我订一张去上海的高铁票"
	var speech []int16
	for chunk, err := range synth.Synthesize(ctx, phrase) {
		if err != nil {
			t.Fatal(err)
		}
		speech = append(speech, chunk...)
	}
	speechMs := len(speech) * 1000 / 16000

	for _, model := range []string{"paraformer-realtime-v2", "fun-asr-realtime"} {
		t.Run(model, func(t *testing.T) {
			rec, err := NewDashScope(DashScopeConfig{Conn: conn, Model: model, MaxSentenceSilenceMs: 500})
			if err != nil {
				t.Fatal(err)
			}
			s, err := rec.Open(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()

			// 1s silence, speech, 3s silence, paced at 20ms like WebRTC.
			feed := append(append(make([]int16, 16000), speech...), make([]int16, 48000)...)
			speechEnd := time.Time{}
			var finals []Result
			var firstFinalAt time.Time
			start := time.Now()
			tick := time.NewTicker(20 * time.Millisecond)
			defer tick.Stop()
			for off := 0; off < len(feed); off += 320 {
				<-tick.C
				if err := s.Write(ctx, feed[off:off+320]); err != nil {
					t.Fatal(err)
				}
				if speechEnd.IsZero() && off >= 16000+len(speech) {
					speechEnd = time.Now()
				}
			drain:
				for {
					select {
					case r := <-s.Results():
						if r.Final {
							finals = append(finals, r)
							if firstFinalAt.IsZero() {
								firstFinalAt = time.Now()
							}
						}
					default:
						break drain
					}
				}
			}
			var texts []string
			for _, f := range finals {
				texts = append(texts, f.Text)
			}
			t.Logf("speech=%dms finals=%q end-of-speech→first final=%v (stream elapsed %v)",
				speechMs, texts, firstFinalAt.Sub(speechEnd), time.Since(start).Round(time.Millisecond))
			joined := strings.Join(texts, "")
			for _, kw := range []string{"上海", "高铁"} {
				if !strings.Contains(joined, kw) {
					t.Errorf("missing %q in %q", kw, joined)
				}
			}
		})
	}
}
