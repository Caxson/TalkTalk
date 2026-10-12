package dashscope

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// fakeServer runs script for every accepted connection.
func fakeServer(t *testing.T, script func(ctx context.Context, c *websocket.Conn, r *http.Request)) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		script(r.Context(), c, r)
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

type inMsg struct {
	Header  header         `json:"header"`
	Payload map[string]any `json:"payload"`
}

func readClient(ctx context.Context, c *websocket.Conn) (inMsg, []byte, error) {
	typ, b, err := c.Read(ctx)
	if err != nil {
		return inMsg{}, nil, err
	}
	if typ == websocket.MessageBinary {
		return inMsg{}, b, nil
	}
	var m inMsg
	return m, nil, json.Unmarshal(b, &m)
}

func sendEvent(ctx context.Context, c *websocket.Conn, taskID, event string, output any, errCode string) {
	m := map[string]any{"header": map[string]any{"task_id": taskID, "event": event}, "payload": map[string]any{}}
	if output != nil {
		m["payload"] = map[string]any{"output": output}
	}
	if errCode != "" {
		m["header"].(map[string]any)["error_code"] = errCode
		m["header"].(map[string]any)["error_message"] = "boom"
	}
	b, _ := json.Marshal(m)
	c.Write(ctx, websocket.MessageText, b)
}

// echoServer: on run-task → started; on continue-task → one result and one
// binary frame echoing the text; on finish-task → finished. Loops forever.
func echoServer(t *testing.T, gotAuth chan<- string) string {
	return fakeServer(t, func(ctx context.Context, c *websocket.Conn, r *http.Request) {
		if gotAuth != nil {
			gotAuth <- r.Header.Get("Authorization")
		}
		for {
			m, bin, err := readClient(ctx, c)
			if err != nil {
				return
			}
			if bin != nil {
				continue
			}
			id := m.Header.TaskID
			switch m.Header.Action {
			case "run-task":
				sendEvent(ctx, c, id, evStarted, nil, "")
			case "continue-task":
				text := m.Payload["input"].(map[string]any)["text"].(string)
				sendEvent(ctx, c, id, evResult, map[string]any{"text": text}, "")
				c.Write(ctx, websocket.MessageBinary, []byte(text))
			case "finish-task":
				sendEvent(ctx, c, id, evFinished, nil, "")
			}
		}
	})
}

func dial(t *testing.T, url string) *Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Dial(ctx, Config{URL: url, APIKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func drain(t *testing.T, task *Task) ([]Event, error) {
	t.Helper()
	var evs []Event
	timeout := time.After(3 * time.Second)
	for {
		select {
		case e, ok := <-task.Events():
			if !ok {
				t.Fatal("events closed without KindEnd")
			}
			if e.Kind == KindEnd {
				if _, more := <-task.Events(); more {
					t.Error("events not closed after KindEnd")
				}
				return evs, e.Err
			}
			evs = append(evs, e)
		case <-timeout:
			t.Fatal("task did not end")
		}
	}
}

var spec = TaskSpec{TaskGroup: "audio", Task: "tts", Function: "SpeechSynthesizer", Model: "m"}

func TestTaskRoundTripAndConnectionReuse(t *testing.T) {
	authCh := make(chan string, 1)
	c := dial(t, echoServer(t, authCh))
	ctx := context.Background()
	if auth := <-authCh; auth != "Bearer secret" {
		t.Errorf("Authorization = %q", auth)
	}

	for run := 0; run < 2; run++ {
		task, err := c.Start(ctx, spec)
		if err != nil {
			t.Fatalf("run %d Start: %v", run, err)
		}
		if !c.Busy() {
			t.Error("Busy = false during task")
		}
		if err := task.SendText(ctx, "你好"); err != nil {
			t.Fatal(err)
		}
		if err := task.Finish(ctx, false); err != nil {
			t.Fatal(err)
		}
		evs, err := drain(t, task)
		if err != nil {
			t.Fatalf("run %d ended with %v", run, err)
		}
		if len(evs) != 2 || evs[0].Kind != KindResult || evs[1].Kind != KindAudio || string(evs[1].Audio) != "你好" {
			t.Errorf("run %d events = %+v", run, evs)
		}
		var out struct{ Text string }
		json.Unmarshal(evs[0].Output, &out)
		if out.Text != "你好" {
			t.Errorf("result output = %s", evs[0].Output)
		}
		if c.Busy() || !c.Alive() {
			t.Errorf("after finish: Busy=%v Alive=%v", c.Busy(), c.Alive())
		}
	}
}

func TestTaskFailedRetiresConnection(t *testing.T) {
	url := fakeServer(t, func(ctx context.Context, c *websocket.Conn, _ *http.Request) {
		m, _, _ := readClient(ctx, c)
		sendEvent(ctx, c, m.Header.TaskID, evStarted, nil, "")
		readClient(ctx, c) // continue-task
		sendEvent(ctx, c, m.Header.TaskID, evFailed, nil, "InvalidParameter")
		readClient(ctx, c)
	})
	c := dial(t, url)
	ctx := context.Background()
	task, err := c.Start(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	task.SendText(ctx, "x")
	_, err = drain(t, task)
	var te *TaskError
	if !errors.As(err, &te) || te.Code != "InvalidParameter" {
		t.Fatalf("end err = %v, want TaskError", err)
	}
	if c.Alive() {
		t.Error("connection still alive after task-failed")
	}
	if _, err := c.Start(ctx, spec); err == nil {
		t.Error("Start on dead connection succeeded")
	}
}

func TestFailureBeforeStartIsReturnedByStart(t *testing.T) {
	url := fakeServer(t, func(ctx context.Context, c *websocket.Conn, _ *http.Request) {
		m, _, _ := readClient(ctx, c)
		sendEvent(ctx, c, m.Header.TaskID, evFailed, nil, "ModelNotFound")
		readClient(ctx, c)
	})
	_, err := dial(t, url).Start(context.Background(), spec)
	var te *TaskError
	if !errors.As(err, &te) || te.Code != "ModelNotFound" {
		t.Errorf("Start err = %v", err)
	}
}

func TestStaleEventsAreIgnored(t *testing.T) {
	url := fakeServer(t, func(ctx context.Context, c *websocket.Conn, _ *http.Request) {
		m, _, _ := readClient(ctx, c)
		id := m.Header.TaskID
		sendEvent(ctx, c, "old-task", evStarted, nil, "")
		sendEvent(ctx, c, id, evStarted, nil, "")
		sendEvent(ctx, c, "old-task", evResult, map[string]any{"text": "stale"}, "")
		sendEvent(ctx, c, "old-task", evFailed, nil, "Stale")
		sendEvent(ctx, c, id, evResult, map[string]any{"text": "fresh"}, "")
		sendEvent(ctx, c, id, evFinished, nil, "")
		readClient(ctx, c)
	})
	c := dial(t, url)
	task, err := c.Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	evs, err := drain(t, task)
	if err != nil || len(evs) != 1 || !strings.Contains(string(evs[0].Output), "fresh") {
		t.Errorf("events %+v err %v", evs, err)
	}
	if !c.Alive() {
		t.Error("stale task-failed killed the connection")
	}
}

func TestServerDisconnectEndsTask(t *testing.T) {
	url := fakeServer(t, func(ctx context.Context, c *websocket.Conn, _ *http.Request) {
		m, _, _ := readClient(ctx, c)
		sendEvent(ctx, c, m.Header.TaskID, evStarted, nil, "")
		c.Close(websocket.StatusGoingAway, "bye")
	})
	c := dial(t, url)
	task, err := c.Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := drain(t, task); !errors.Is(err, ErrConnClosed) {
		t.Errorf("end err = %v, want ErrConnClosed", err)
	}
}

func TestStartTimesOut(t *testing.T) {
	url := fakeServer(t, func(ctx context.Context, c *websocket.Conn, _ *http.Request) {
		readClient(ctx, c)
		<-ctx.Done()
	})
	c := dial(t, url)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := c.Start(ctx, spec); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Start err = %v, want deadline", err)
	}
	if c.Alive() {
		t.Error("connection in unknown state should be retired")
	}
}

func TestStartWhileBusy(t *testing.T) {
	c := dial(t, echoServer(t, nil))
	if _, err := c.Start(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Start(context.Background(), spec); err == nil {
		t.Error("second concurrent Start succeeded")
	}
}

func TestCancelDirectiveIsSent(t *testing.T) {
	got := make(chan map[string]any, 1)
	url := fakeServer(t, func(ctx context.Context, c *websocket.Conn, _ *http.Request) {
		m, _, _ := readClient(ctx, c)
		sendEvent(ctx, c, m.Header.TaskID, evStarted, nil, "")
		fin, _, _ := readClient(ctx, c)
		got <- fin.Payload
		sendEvent(ctx, c, m.Header.TaskID, evFinished, nil, "")
		readClient(ctx, c)
	})
	c := dial(t, url)
	task, _ := c.Start(context.Background(), spec)
	task.Finish(context.Background(), true)
	p := <-got
	if p["input"].(map[string]any)["directive"] != "cancel" {
		t.Errorf("finish payload = %v", p)
	}
	drain(t, task)
}

func TestDialErrors(t *testing.T) {
	if _, err := Dial(context.Background(), Config{}); err == nil {
		t.Error("Dial without key succeeded")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer srv.Close()
	_, err := Dial(context.Background(), Config{URL: "ws" + strings.TrimPrefix(srv.URL, "http"), APIKey: "k"})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("Dial err = %v, want 401", err)
	}
}

func TestMalformedEventKillsConnection(t *testing.T) {
	url := fakeServer(t, func(ctx context.Context, c *websocket.Conn, _ *http.Request) {
		m, _, _ := readClient(ctx, c)
		sendEvent(ctx, c, m.Header.TaskID, evStarted, nil, "")
		c.Write(ctx, websocket.MessageText, []byte("{oops"))
		readClient(ctx, c)
	})
	c := dial(t, url)
	task, err := c.Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := drain(t, task); err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Errorf("end err = %v", err)
	}
}
