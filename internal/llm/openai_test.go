package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func sseServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(handler))
	t.Cleanup(srv.Close)
	return srv
}

func writeChunks(w http.ResponseWriter, chunks ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, c := range chunks {
		fmt.Fprintf(w, "data: %s\n\n", c)
		w.(http.Flusher).Flush()
	}
}

func delta(s string) string {
	b, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": s}}},
	})
	return string(b)
}

func collect(t *testing.T, s Streamer, ctx context.Context) (string, error) {
	t.Helper()
	var sb strings.Builder
	for d, err := range s.Stream(ctx, []Message{{Role: RoleUser, Content: "hi"}}) {
		if err != nil {
			return sb.String(), err
		}
		sb.WriteString(d)
	}
	return sb.String(), nil
}

func newTestClient(t *testing.T, baseURL string, extra map[string]any) *OpenAIClient {
	t.Helper()
	c, err := NewOpenAIClient(OpenAIConfig{BaseURL: baseURL, APIKey: "k", Model: "m", ExtraBody: extra})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestStreamConcatenatesDeltas(t *testing.T) {
	var gotBody map[string]any
	var gotAuth string
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" || r.Method != http.MethodPost {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&gotBody)
		writeChunks(w,
			`{"choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`,
			delta("你"), delta("好"),
			`{"choices":[],"usage":{"total_tokens":3}}`,
			"[DONE]")
	})
	c := newTestClient(t, srv.URL, map[string]any{"thinking": map[string]any{"type": "disabled"}})

	got, err := collect(t, c, context.Background())
	if err != nil {
		t.Fatalf("stream error: %v", err)
	}
	if got != "你好" {
		t.Errorf("text = %q, want 你好", got)
	}
	if gotAuth != "Bearer k" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotBody["model"] != "m" || gotBody["stream"] != true {
		t.Errorf("body = %v", gotBody)
	}
	if th, _ := gotBody["thinking"].(map[string]any); th["type"] != "disabled" {
		t.Errorf("extra body not merged: %v", gotBody)
	}
}

func TestExtraBodyCannotOverrideCoreFields(t *testing.T) {
	var gotBody map[string]any
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		writeChunks(w, "[DONE]")
	})
	c := newTestClient(t, srv.URL, map[string]any{"model": "evil", "stream": false, "messages": "x"})
	if _, err := collect(t, c, context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotBody["model"] != "m" || gotBody["stream"] != true {
		t.Errorf("core fields overridden: %v", gotBody)
	}
	if _, ok := gotBody["messages"].([]any); !ok {
		t.Errorf("messages overridden: %v", gotBody["messages"])
	}
}

func TestStreamHTTPErrorIncludesStatusAndBody(t *testing.T) {
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"bad key"}}`)
	})
	_, err := collect(t, newTestClient(t, srv.URL, nil), context.Background())
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "bad key") {
		t.Errorf("err = %v, want status and body", err)
	}
}

func TestStreamErrorEventMidStream(t *testing.T) {
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeChunks(w, delta("a"), `{"error":{"message":"overloaded"}}`)
	})
	got, err := collect(t, newTestClient(t, srv.URL, nil), context.Background())
	if got != "a" || err == nil || !strings.Contains(err.Error(), "overloaded") {
		t.Errorf("got %q, err %v", got, err)
	}
}

func TestStreamMalformedChunk(t *testing.T) {
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeChunks(w, delta("a"), `{not json`)
	})
	_, err := collect(t, newTestClient(t, srv.URL, nil), context.Background())
	if err == nil {
		t.Error("want error on malformed chunk")
	}
}

func TestStreamEndsWithoutDoneIsError(t *testing.T) {
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeChunks(w, delta("a"))
	})
	got, err := collect(t, newTestClient(t, srv.URL, nil), context.Background())
	if got != "a" || !errors.Is(err, ErrTruncated) {
		t.Errorf("got %q, err %v, want ErrTruncated", got, err)
	}
}

func TestStreamIgnoresCommentsAndOtherFields(t *testing.T) {
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, ": keep-alive\n\nevent: message\nid: 1\ndata: "+delta("x")+"\n\ndata: [DONE]\n\n")
	})
	got, err := collect(t, newTestClient(t, srv.URL, nil), context.Background())
	if err != nil || got != "x" {
		t.Errorf("got %q, err %v", got, err)
	}
}

func TestStreamCancelStopsPromptly(t *testing.T) {
	release := make(chan struct{})
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeChunks(w, delta("a"))
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	c := newTestClient(t, srv.URL, nil)
	done := make(chan error, 1)
	go func() {
		for d, err := range c.Stream(ctx, []Message{{Role: RoleUser, Content: "hi"}}) {
			if err != nil {
				done <- err
				return
			}
			if d == "a" {
				cancel()
			}
		}
		done <- nil
	}()
	select {
	case err := <-done:
		if err == nil || !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not stop after cancel")
	}
}

func TestBreakingEarlyIsSafe(t *testing.T) {
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeChunks(w, delta("a"), delta("b"), "[DONE]")
	})
	c := newTestClient(t, srv.URL, nil)
	for d := range c.Stream(context.Background(), nil) {
		if d == "a" {
			break
		}
	}
}

func TestNewOpenAIClientValidates(t *testing.T) {
	for _, cfg := range []OpenAIConfig{
		{APIKey: "k", Model: "m"},
		{BaseURL: "http://x", Model: "m"},
		{BaseURL: "http://x", APIKey: "k"},
		{BaseURL: "::bad", APIKey: "k", Model: "m"},
	} {
		if _, err := NewOpenAIClient(cfg); err == nil {
			t.Errorf("NewOpenAIClient(%+v) = nil error", cfg)
		}
	}
}
