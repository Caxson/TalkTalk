package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/url"
	"strings"

	"github.com/Caxson/TalkTalk/internal/netx"
)

// ErrTruncated means the stream ended without the [DONE] sentinel, so the
// reply may be incomplete.
var ErrTruncated = errors.New("llm: stream ended without [DONE]")

const maxErrBody = 512

// OpenAIConfig targets any OpenAI-compatible /chat/completions endpoint
// (DeepSeek, Volcengine Ark, DashScope compatible-mode, ...).
type OpenAIConfig struct {
	BaseURL string
	APIKey  string
	Model   string
	// ExtraBody is merged into every request body, for provider-specific
	// switches such as {"thinking":{"type":"disabled"}}. It cannot override
	// model, messages or stream.
	ExtraBody   map[string]any
	UseEnvProxy bool
}

// OpenAIClient streams chat completions over server-sent events.
type OpenAIClient struct {
	endpoint string
	cfg      OpenAIConfig
	http     *http.Client
}

var _ Streamer = (*OpenAIClient)(nil)

// NewOpenAIClient validates cfg and returns a client.
func NewOpenAIClient(cfg OpenAIConfig) (*OpenAIClient, error) {
	if cfg.BaseURL == "" || cfg.APIKey == "" || cfg.Model == "" {
		return nil, errors.New("llm: BaseURL, APIKey and Model are required")
	}
	u, err := url.Parse(strings.TrimRight(cfg.BaseURL, "/") + "/chat/completions")
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("llm: invalid BaseURL %q", cfg.BaseURL)
	}
	return &OpenAIClient{endpoint: u.String(), cfg: cfg, http: netx.NewHTTPClient(cfg.UseEnvProxy)}, nil
}

// Stream implements Streamer.
func (c *OpenAIClient) Stream(ctx context.Context, messages []Message) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		resp, err := c.post(ctx, messages)
		if err != nil {
			yield("", err)
			return
		}
		defer resp.Body.Close()

		for d, err := range parseSSE(resp.Body) {
			if err != nil && ctx.Err() != nil {
				err = fmt.Errorf("llm: %w", ctx.Err())
			}
			if !yield(d, err) || err != nil {
				return
			}
		}
	}
}

func (c *OpenAIClient) post(ctx context.Context, messages []Message) (*http.Response, error) {
	body := make(map[string]any, len(c.cfg.ExtraBody)+3)
	for k, v := range c.cfg.ExtraBody {
		body[k] = v
	}
	if messages == nil {
		messages = []Message{}
	}
	body["model"] = c.cfg.Model
	body["messages"] = messages
	body["stream"] = true

	buf, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("llm: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("llm: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("llm: request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
		return nil, fmt.Errorf("llm: HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(snippet))
	}
	return resp, nil
}

// chunk is the subset of a streamed completion we read. Unknown fields
// (reasoning_content, usage, logprobs, ...) are ignored.
type chunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// parseSSE yields content deltas from an SSE body until [DONE].
func parseSSE(r io.Reader) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for sc.Scan() {
			payload, ok := strings.CutPrefix(sc.Text(), "data:")
			if !ok {
				continue // blank separators, comments, event:/id: fields
			}
			payload = strings.TrimSpace(payload)
			if payload == "[DONE]" {
				return
			}
			var ch chunk
			if err := json.Unmarshal([]byte(payload), &ch); err != nil {
				yield("", fmt.Errorf("llm: malformed chunk: %w", err))
				return
			}
			if ch.Error != nil {
				yield("", fmt.Errorf("llm: provider error: %s", ch.Error.Message))
				return
			}
			for _, c := range ch.Choices {
				if c.Delta.Content != "" && !yield(c.Delta.Content, nil) {
					return
				}
			}
		}
		if err := sc.Err(); err != nil {
			yield("", fmt.Errorf("llm: read stream: %w", err))
			return
		}
		yield("", ErrTruncated)
	}
}
