package photoreal

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeWorker speaks the worker protocol: READY after START, then one
// "frame" per 640 samples received, carrying the frame's index.
type fakeWorker struct {
	ln       net.Listener
	mu       sync.Mutex
	start    map[string]string
	samples  int
	stopped  bool
	errAfter int // send an error after this many frames (0: never)
}

func newFakeWorker(t *testing.T) *fakeWorker {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	w := &fakeWorker{ln: ln}
	t.Cleanup(func() { ln.Close() })
	go w.serve()
	return w
}

func (w *fakeWorker) serve() {
	conn, err := w.ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	r := bufio.NewReader(conn)
	var hdr [5]byte
	frames := 0
	for {
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return
		}
		payload := make([]byte, binary.BigEndian.Uint32(hdr[1:]))
		if _, err := io.ReadFull(r, payload); err != nil {
			return
		}
		switch hdr[0] {
		case msgStart:
			w.mu.Lock()
			json.Unmarshal(payload, &w.start)
			w.mu.Unlock()
			info, _ := json.Marshal(Info{Width: 720, Height: 1280, FPS: 25})
			conn.Write(frame(msgReady, info))
		case msgAudio:
			w.mu.Lock()
			before := w.samples / 640
			w.samples += len(payload) / 2
			after := w.samples / 640
			w.mu.Unlock()
			for k := before; k < after; k++ {
				p := make([]byte, 4, 8)
				binary.BigEndian.PutUint32(p, uint32(k))
				conn.Write(frame(msgVideo, append(p, "AU"...)))
				frames++
				if w.errAfter > 0 && frames == w.errAfter {
					e, _ := json.Marshal(map[string]string{"error": "CUDA out of memory"})
					conn.Write(frame(msgError, e))
				}
			}
		case msgStop:
			w.mu.Lock()
			w.stopped = true
			w.mu.Unlock()
			return
		}
	}
}

func TestStreamsAudioAndReceivesNumberedFrames(t *testing.T) {
	w := newFakeWorker(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Dial(ctx, w.ln.Addr().String(), "rin", 24000, nil)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var got []int64
	var ready Info
	done := make(chan error, 1)
	go func() {
		done <- c.Run(ctx, Handlers{
			OnReady: func(i Info) { mu.Lock(); ready = i; mu.Unlock() },
			OnFrame: func(k int64, au []byte) {
				if string(au) != "AU" {
					t.Errorf("payload %q", au)
				}
				mu.Lock()
				got = append(got, k)
				mu.Unlock()
			},
		})
	}()
	for i := 0; i < 50; i++ { // 1 s of 20 ms chunks at 24 kHz
		c.Feed(make([]int16, 480))
	}
	// Close once the worker has the second's audio and we have its frames:
	// closing drops whatever is still queued.
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		w.mu.Lock()
		have := w.samples
		w.mu.Unlock()
		if (n >= 24 && have >= 16000-640) || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	c.Close()
	if err := <-done; err != nil {
		t.Errorf("Run: %v", err)
	}
	// STOP is on the wire once Close returns; give the worker a moment to read it.
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		w.mu.Lock()
		stopped := w.stopped
		w.mu.Unlock()
		if stopped {
			break
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	mu.Lock()
	defer mu.Unlock()
	if w.start["character"] != "rin" {
		t.Errorf("start = %v", w.start)
	}
	// The resampler holds back a few samples for its filter; over 1 s the
	// worker must have received 16 kHz audio, within a frame.
	if w.samples < 16000-640 || w.samples > 16000 {
		t.Errorf("worker got %d samples for 1 s", w.samples)
	}
	if ready.Width != 720 || ready.FPS != 25 {
		t.Errorf("ready = %+v", ready)
	}
	for i, k := range got {
		if k != int64(i) {
			t.Fatalf("frame %d has index %d", i, k)
		}
	}
	if !w.stopped {
		t.Error("worker not told to stop")
	}
	c.Close() // a second Close is harmless
}

func TestWorkerErrorEndsRun(t *testing.T) {
	w := newFakeWorker(t)
	w.errAfter = 2
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Dial(ctx, w.ln.Addr().String(), "yuna", 16000, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx, Handlers{}) }()
	for i := 0; i < 10; i++ {
		c.Feed(make([]int16, 320))
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "CUDA out of memory") {
			t.Errorf("Run = %v", err)
		}
	case <-ctx.Done():
		t.Fatal("Run did not end on a worker error")
	}
}

func TestDialFailsFast(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	if _, err := Dial(context.Background(), addr, "rin", 24000, nil); err == nil {
		t.Error("dial to a closed port succeeded")
	}
}
