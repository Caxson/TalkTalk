package dashscope

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/coder/websocket"
)

// Kind of task event.
type Kind int

const (
	KindResult Kind = iota + 1 // result-generated; Output holds payload.output
	KindAudio                  // binary frame; Audio holds the bytes
	KindEnd                    // terminal; Err is nil on task-finished
)

// Event is one item from a task's event stream.
type Event struct {
	Kind   Kind
	Output json.RawMessage
	Audio  []byte
	Err    error
}

// TaskError is a task-failed event from the server.
type TaskError struct {
	Code    string
	Message string
}

func (e *TaskError) Error() string {
	return fmt.Sprintf("dashscope: task failed: %s: %s", e.Code, e.Message)
}

// Task is one run on a Conn. Its event stream ends with exactly one KindEnd
// event, after which the channel is closed.
type Task struct {
	id     string
	conn   *Conn
	events chan Event

	started   chan error
	startOnce sync.Once
	endOnce   sync.Once
	mu        sync.Mutex
	ended     bool
}

// ID is the task id.
func (t *Task) ID() string { return t.id }

// Events streams results, audio and the terminal event.
func (t *Task) Events() <-chan Event { return t.events }

// SendAudio sends one binary audio frame.
func (t *Task) SendAudio(ctx context.Context, pcm []byte) error {
	return t.conn.write(ctx, websocket.MessageBinary, pcm)
}

// SendText sends a continue-task carrying text (TTS input).
func (t *Task) SendText(ctx context.Context, text string) error {
	return t.send(ctx, "continue-task", map[string]any{"text": text})
}

// Finish sends finish-task. With cancel, the server stops generating
// immediately instead of flushing buffered input.
func (t *Task) Finish(ctx context.Context, cancel bool) error {
	input := map[string]any{}
	if cancel {
		input["directive"] = "cancel"
	}
	return t.send(ctx, "finish-task", input)
}

func (t *Task) send(ctx context.Context, action string, input map[string]any) error {
	return t.conn.writeJSON(ctx, clientMsg{
		Header:  header{Action: action, TaskID: t.id, Streaming: "duplex"},
		Payload: map[string]any{"input": input},
	})
}

func (t *Task) markStarted(err error) {
	t.startOnce.Do(func() { t.started <- err })
}

// push delivers a non-terminal event. If the consumer has fallen a full
// buffer behind, the task is failed rather than blocking the reader, which
// would stall every other task's traffic on the connection.
func (t *Task) push(e Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ended {
		return
	}
	select {
	case t.events <- e:
	default:
		go t.conn.fail(fmt.Errorf("dashscope: task %s consumer too slow", t.id))
	}
}

func (t *Task) end(err error) {
	t.endOnce.Do(func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		t.ended = true
		// Terminal event must not be lost: the buffer always has room
		// for it unless the consumer is overflowing, in which case the
		// close alone still signals the end.
		select {
		case t.events <- Event{Kind: KindEnd, Err: err}:
		default:
		}
		close(t.events)
	})
}
