// Package dashscopetest provides a scriptable fake of the DashScope duplex
// task protocol for unit tests.
package dashscopetest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/coder/websocket"
)

// Task is the server side of one run-task. Handlers use it to reply.
type Task struct {
	ID     string
	Model  string
	Params map[string]any
	ctx    context.Context
	c      *websocket.Conn
}

func (t *Task) event(event string, extra map[string]any, output any) {
	h := map[string]any{"task_id": t.ID, "event": event}
	for k, v := range extra {
		h[k] = v
	}
	m := map[string]any{"header": h, "payload": map[string]any{}}
	if output != nil {
		m["payload"] = map[string]any{"output": output}
	}
	b, _ := json.Marshal(m)
	t.c.Write(t.ctx, websocket.MessageText, b)
}

// Started sends task-started.
func (t *Task) Started() { t.event("task-started", nil, nil) }

// Result sends result-generated with the given payload.output.
func (t *Task) Result(output any) { t.event("result-generated", nil, output) }

// Audio sends a binary frame.
func (t *Task) Audio(b []byte) { t.c.Write(t.ctx, websocket.MessageBinary, b) }

// Finished sends task-finished.
func (t *Task) Finished() { t.event("task-finished", nil, nil) }

// Failed sends task-failed.
func (t *Task) Failed(code, msg string) {
	t.event("task-failed", map[string]any{"error_code": code, "error_message": msg}, nil)
}

// Handlers script the fake. Nil handlers fall back to defaults: run-task
// replies task-started, finish-task replies task-finished, others no-op.
type Handlers struct {
	Run    func(t *Task)
	Text   func(t *Task, text string)
	Audio  func(t *Task, pcm []byte)
	Finish func(t *Task, cancel bool)
}

// Server is a running fake.
type Server struct {
	URL string

	mu    sync.Mutex
	conns int
	tasks int
}

// Conns is the number of WebSocket connections accepted so far.
func (s *Server) Conns() int { s.mu.Lock(); defer s.mu.Unlock(); return s.conns }

// Tasks is the number of run-task messages received so far.
func (s *Server) Tasks() int { s.mu.Lock(); defer s.mu.Unlock(); return s.tasks }

// New starts a fake server closed at test cleanup.
func New(t testing.TB, h Handlers) *Server {
	t.Helper()
	s := &Server{}
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		c.SetReadLimit(8 << 20)
		s.mu.Lock()
		s.conns++
		s.mu.Unlock()
		s.serve(r.Context(), c, h)
	}))
	t.Cleanup(hs.Close)
	s.URL = "ws" + strings.TrimPrefix(hs.URL, "http")
	return s
}

type clientMsg struct {
	Header struct {
		Action string `json:"action"`
		TaskID string `json:"task_id"`
	} `json:"header"`
	Payload struct {
		Model      string         `json:"model"`
		Parameters map[string]any `json:"parameters"`
		Input      map[string]any `json:"input"`
	} `json:"payload"`
}

func (s *Server) serve(ctx context.Context, c *websocket.Conn, h Handlers) {
	var cur *Task
	for {
		typ, b, err := c.Read(ctx)
		if err != nil {
			return
		}
		if typ == websocket.MessageBinary {
			if cur != nil && h.Audio != nil {
				h.Audio(cur, b)
			}
			continue
		}
		var m clientMsg
		if json.Unmarshal(b, &m) != nil {
			return
		}
		switch m.Header.Action {
		case "run-task":
			s.mu.Lock()
			s.tasks++
			s.mu.Unlock()
			cur = &Task{ID: m.Header.TaskID, Model: m.Payload.Model, Params: m.Payload.Parameters, ctx: ctx, c: c}
			if h.Run != nil {
				h.Run(cur)
			} else {
				cur.Started()
			}
		case "continue-task":
			if h.Text != nil && cur != nil {
				text, _ := m.Payload.Input["text"].(string)
				h.Text(cur, text)
			}
		case "finish-task":
			if cur == nil {
				continue
			}
			cancel := m.Payload.Input["directive"] == "cancel"
			if h.Finish != nil {
				h.Finish(cur, cancel)
			} else {
				cur.Finished()
			}
		}
	}
}
