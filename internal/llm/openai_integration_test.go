package llm

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// Run with: TALKTALK_IT=1 go test ./internal/llm -run Integration -v
// Requires DEEPSEEK_API_KEY. Hits the real DeepSeek API.
func TestIntegrationDeepSeekStream(t *testing.T) {
	if os.Getenv("TALKTALK_IT") == "" {
		t.Skip("set TALKTALK_IT=1 to hit real providers")
	}
	key := os.Getenv("DEEPSEEK_API_KEY")
	if key == "" {
		t.Fatal("DEEPSEEK_API_KEY not set")
	}
	c, err := NewOpenAIClient(OpenAIConfig{
		BaseURL:   "https://api.deepseek.com",
		APIKey:    key,
		Model:     "deepseek-flash",
		ExtraBody: map[string]any{"thinking": map[string]any{"type": "disabled"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	start := time.Now()
	var first time.Duration
	var sb strings.Builder
	for d, err := range c.Stream(ctx, []Message{
		{Role: RoleSystem, Content: "只用一句中文回答。"},
		{Role: RoleUser, Content: "一加一等于几？"},
	}) {
		if err != nil {
			t.Fatalf("stream: %v", err)
		}
		if first == 0 {
			first = time.Since(start)
		}
		sb.WriteString(d)
	}
	t.Logf("ttft=%v total=%v reply=%q", first, time.Since(start), sb.String())
	if !strings.ContainsAny(sb.String(), "2二") {
		t.Errorf("unexpected reply %q", sb.String())
	}
}
