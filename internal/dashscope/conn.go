package dashscope

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/Caxson/TalkTalk/internal/netx"
)

// ErrConnClosed is returned when the connection dies under a task.
var ErrConnClosed = errors.New("dashscope: connection closed")

const (
	startTimeout = 10 * time.Second
	readLimit    = 8 << 20
	eventBuffer  = 256
)

// Config for dialing.
type Config struct {
	URL         string // defaults to DefaultURL
	APIKey      string
	UseEnvProxy bool
}

// Conn is one WebSocket connection running at most one task at a time.
type Conn struct {
	ws *websocket.Conn

	writeMu sync.Mutex

	mu      sync.Mutex
	current *Task
	err     error // terminal connection error, set once
	done    chan struct{}
}

// Dial opens a connection. ctx bounds only the handshake.
func Dial(ctx context.Context, cfg Config) (*Conn, error) {
	if cfg.APIKey == "" {
		return nil, errors.New("dashscope: APIKey is required")
	}
	url := cfg.URL
	if url == "" {
		url = DefaultURL
	}
	ws, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPClient: netx.NewWebSocketHTTPClient(cfg.UseEnvProxy),
		HTTPHeader: http.Header{"Authorization": {"Bearer " + cfg.APIKey}},
	})
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("dashscope: dial: HTTP %d: %w", resp.StatusCode, err)
		}
		return nil, fmt.Errorf("dashscope: dial: %w", err)
	}
	ws.SetReadLimit(readLimit)
	c := &Conn{ws: ws, done: make(chan struct{})}
	go c.readLoop()
	return c, nil
}

// Alive reports whether the connection can still run tasks.
func (c *Conn) Alive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err == nil
}

// Busy reports whether a task is in flight.
func (c *Conn) Busy() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current != nil
}

// Close tears the connection down; a running task fails with ErrConnClosed.
func (c *Conn) Close() error {
	c.fail(ErrConnClosed)
	return c.ws.Close(websocket.StatusNormalClosure, "")
}

// abort retires the connection immediately after a protocol-level failure.
func (c *Conn) abort(err error) {
	c.fail(err)
	c.ws.CloseNow()
}

// Start sends run-task and waits for task-started.
func (c *Conn) Start(ctx context.Context, spec TaskSpec) (*Task, error) {
	t := &Task{id: uuid.NewString(), conn: c, events: make(chan Event, eventBuffer), started: make(chan error, 1)}

	c.mu.Lock()
	switch {
	case c.err != nil:
		err := c.err
		c.mu.Unlock()
		return nil, err
	case c.current != nil:
		c.mu.Unlock()
		return nil, errors.New("dashscope: connection already running a task")
	}
	c.current = t
	c.mu.Unlock()

	params := spec.Parameters
	if params == nil {
		params = map[string]any{}
	}
	err := c.writeJSON(ctx, clientMsg{
		Header: header{Action: "run-task", TaskID: t.id, Streaming: "duplex"},
		Payload: map[string]any{
			"task_group": spec.TaskGroup, "task": spec.Task, "function": spec.Function,
			"model": spec.Model, "parameters": params, "input": map[string]any{},
		},
	})
	if err != nil {
		c.detach(t)
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()
	select {
	case err := <-t.started:
		if err != nil {
			return nil, err
		}
		return t, nil
	case <-ctx.Done():
		// The server may still start the task; the connection is in an
		// unknown state, so retire it without waiting on a close handshake.
		c.abort(fmt.Errorf("dashscope: start timed out: %w", ctx.Err()))
		return nil, fmt.Errorf("dashscope: waiting for task-started: %w", ctx.Err())
	}
}

func (c *Conn) writeJSON(ctx context.Context, m clientMsg) error {
	b, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("dashscope: encode: %w", err)
	}
	return c.write(ctx, websocket.MessageText, b)
}

func (c *Conn) write(ctx context.Context, typ websocket.MessageType, b []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.ws.Write(ctx, typ, b); err != nil {
		err = fmt.Errorf("dashscope: write: %w", err)
		c.fail(err)
		return err
	}
	return nil
}

func (c *Conn) readLoop() {
	ctx := context.Background()
	for {
		typ, data, err := c.ws.Read(ctx)
		if err != nil {
			c.fail(fmt.Errorf("%w: %v", ErrConnClosed, err))
			return
		}
		if typ == websocket.MessageBinary {
			if t := c.task(); t != nil {
				t.push(Event{Kind: KindAudio, Audio: data})
			}
			continue
		}
		var m serverMsg
		if err := json.Unmarshal(data, &m); err != nil {
			c.abort(fmt.Errorf("dashscope: malformed event: %w", err))
			return
		}
		c.dispatch(m)
	}
}

func (c *Conn) dispatch(m serverMsg) {
	t := c.task()
	if t == nil || m.Header.TaskID != t.id {
		return // stale event from a cancelled or previous task
	}
	switch m.Header.Event {
	case evStarted:
		t.markStarted(nil)
	case evResult:
		t.push(Event{Kind: KindResult, Output: m.Payload.Output})
	case evFinished:
		c.detach(t)
		t.end(nil)
	case evFailed:
		err := &TaskError{Code: m.Header.ErrorCode, Message: m.Header.ErrorMessage}
		// The server will not accept further tasks on this connection.
		c.fail(err)
	}
}

func (c *Conn) task() *Task {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}

func (c *Conn) detach(t *Task) {
	c.mu.Lock()
	if c.current == t {
		c.current = nil
	}
	c.mu.Unlock()
}

// fail marks the connection dead and ends the running task with err.
func (c *Conn) fail(err error) {
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return
	}
	c.err = err
	t := c.current
	c.current = nil
	close(c.done)
	c.mu.Unlock()
	if t != nil {
		t.markStarted(err)
		t.end(err)
	}
}
