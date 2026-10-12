package playout

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Caxson/TalkTalk/internal/audio"
)

const rate = 16000 // 320 samples per 20ms frame

type sinkRec struct {
	mu      sync.Mutex
	packets int
	durs    []time.Duration
	err     error
}

func (s *sinkRec) write(pkt []byte, d time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(pkt) == 0 {
		panic("empty packet")
	}
	s.packets++
	s.durs = append(s.durs, d)
	return s.err
}

func (s *sinkRec) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.packets
}

type harness struct {
	p     *Player
	sink  *sinkRec
	ticks chan time.Time
	done  chan error
	stop  context.CancelFunc
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	sink := &sinkRec{}
	p, err := New(rate, sink.write)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := &harness{p: p, sink: sink, ticks: make(chan time.Time), done: make(chan error, 1), stop: cancel}
	go func() { h.done <- p.run(ctx, h.ticks) }()
	t.Cleanup(func() { cancel(); <-h.done })
	return h
}

// tick advances playout by n frames and waits until each has been sent.
func (h *harness) tick(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		want := h.sink.count() + 1
		h.ticks <- time.Now()
		deadline := time.Now().Add(time.Second)
		for h.sink.count() < want {
			if time.Now().After(deadline) {
				t.Fatal("frame not sent after tick")
			}
			time.Sleep(time.Millisecond)
		}
	}
}

func recvMark(t *testing.T, p *Player) int {
	t.Helper()
	select {
	case id := <-p.Played():
		return id
	case <-time.After(time.Second):
		t.Fatal("no mark played")
		return 0
	}
}

func noMark(t *testing.T, p *Player) {
	t.Helper()
	select {
	case id := <-p.Played():
		t.Fatalf("unexpected mark %d", id)
	default:
	}
}

func TestSendsSilenceWhenIdle(t *testing.T) {
	h := newHarness(t)
	h.tick(t, 3)
	if h.p.Pending() != 0 {
		t.Errorf("Pending = %v, want 0", h.p.Pending())
	}
	for _, d := range h.sink.durs {
		if d != audio.FrameDuration {
			t.Errorf("sample duration %v, want 20ms", d)
		}
	}
}

func TestMarkFiresAfterPrecedingAudio(t *testing.T) {
	h := newHarness(t)
	h.p.Enqueue(make([]int16, 3*320)) // 3 frames
	h.p.Mark(7)
	if got := h.p.Pending(); got != 60*time.Millisecond {
		t.Errorf("Pending = %v, want 60ms", got)
	}

	h.tick(t, 2)
	noMark(t, h.p)
	h.tick(t, 1)
	if id := recvMark(t, h.p); id != 7 {
		t.Errorf("mark = %d, want 7", id)
	}
	if h.p.Pending() != 0 {
		t.Errorf("Pending after drain = %v", h.p.Pending())
	}
}

func TestPartialFrameIsPaddedBeforeMark(t *testing.T) {
	h := newHarness(t)
	h.p.Enqueue(make([]int16, 400)) // 1.25 frames
	h.p.Mark(1)
	h.tick(t, 1)
	noMark(t, h.p)
	h.tick(t, 1)
	if id := recvMark(t, h.p); id != 1 {
		t.Errorf("mark = %d", id)
	}
}

func TestChunksAcrossEnqueueCallsAreContiguous(t *testing.T) {
	h := newHarness(t)
	h.p.Enqueue(make([]int16, 200))
	h.p.Enqueue(make([]int16, 120)) // together exactly one frame
	h.p.Mark(1)
	h.tick(t, 1)
	if id := recvMark(t, h.p); id != 1 {
		t.Errorf("mark = %d", id)
	}
}

func TestMarksOrderedAcrossSegments(t *testing.T) {
	h := newHarness(t)
	h.p.Enqueue(make([]int16, 320))
	h.p.Mark(1)
	h.p.Enqueue(make([]int16, 320))
	h.p.Mark(2)
	h.tick(t, 2)
	if a, b := recvMark(t, h.p), recvMark(t, h.p); a != 1 || b != 2 {
		t.Errorf("marks = %d,%d want 1,2", a, b)
	}
}

func TestFlushDropsAudioAndReturnsUnplayedMarks(t *testing.T) {
	h := newHarness(t)
	h.p.Enqueue(make([]int16, 320))
	h.p.Mark(1)
	h.p.Enqueue(make([]int16, 5*320))
	h.p.Mark(2)
	h.p.Mark(3)

	h.tick(t, 1)
	if id := recvMark(t, h.p); id != 1 {
		t.Fatalf("mark = %d", id)
	}
	dropped := h.p.Flush()
	if len(dropped) != 2 || dropped[0] != 2 || dropped[1] != 3 {
		t.Errorf("Flush = %v, want [2 3]", dropped)
	}
	if h.p.Pending() != 0 {
		t.Errorf("Pending after flush = %v", h.p.Pending())
	}
	h.tick(t, 6)
	noMark(t, h.p)
}

func TestMarkOnEmptyQueueFiresNextTick(t *testing.T) {
	h := newHarness(t)
	h.p.Mark(9)
	h.tick(t, 1)
	if id := recvMark(t, h.p); id != 9 {
		t.Errorf("mark = %d", id)
	}
}

func TestEnqueueDoesNotAliasCaller(t *testing.T) {
	h := newHarness(t)
	buf := make([]int16, 320)
	h.p.Enqueue(buf)
	buf[0] = 1 // must not affect queued audio or panic
	h.tick(t, 1)
}

func TestSinkErrorStopsRun(t *testing.T) {
	sink := &sinkRec{err: errors.New("track closed")}
	p, err := New(rate, sink.write)
	if err != nil {
		t.Fatal(err)
	}
	ticks := make(chan time.Time, 1)
	ticks <- time.Now()
	err = p.run(context.Background(), ticks)
	if err == nil || !errors.Is(err, sink.err) {
		t.Errorf("run = %v, want sink error", err)
	}
}

func TestRunWithRealClock(t *testing.T) {
	sink := &sinkRec{}
	p, err := New(rate, sink.write)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 210*time.Millisecond)
	defer cancel()
	if err := p.Run(ctx); err != nil {
		t.Fatalf("Run = %v", err)
	}
	if n := sink.count(); n < 7 || n > 12 {
		t.Errorf("sent %d frames in ~200ms, want about 10", n)
	}
}

func TestNewRejectsBadRate(t *testing.T) {
	if _, err := New(44100, (&sinkRec{}).write); err == nil {
		t.Error("want error for 44.1k")
	}
}
