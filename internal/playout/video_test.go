package playout

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// aheadRec records what the player fed, played and sent as video, tick by
// tick. Frames are tagged by their first sample, and the test encoder
// passes that tag through as the "packet".
type aheadRec struct {
	mu     sync.Mutex
	fed    []int16 // tag fed per tick (0 = silence)
	played []int16 // tag played per tick (0 = silence)
	video  [][]byte
	vTick  []int // tick index each video frame went out on
}

func newAheadHarness(t *testing.T, ahead int) (*Player, *aheadRec, func(n int)) {
	t.Helper()
	rec := &aheadRec{}
	p, err := New(rate, func(pkt []byte, _ time.Duration) error {
		rec.mu.Lock()
		rec.played = append(rec.played, int16(pkt[0])|int16(pkt[1])<<8)
		rec.mu.Unlock()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	p.encode = func(f []int16) ([]byte, error) { return []byte{byte(f[0]), byte(f[0] >> 8)}, nil }
	p.SetLookahead(ahead, func(pcm []int16) {
		rec.mu.Lock()
		rec.fed = append(rec.fed, pcm[0])
		rec.mu.Unlock()
	}, func(v []byte, _ time.Duration) error {
		rec.mu.Lock()
		rec.video = append(rec.video, v)
		rec.vTick = append(rec.vTick, len(rec.played)-1)
		rec.mu.Unlock()
		return nil
	})
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.run(ctx, ticks) }()
	t.Cleanup(func() { cancel(); <-done })
	tick := func(n int) {
		for i := 0; i < n; i++ {
			rec.mu.Lock()
			want := len(rec.played) + 1
			rec.mu.Unlock()
			ticks <- time.Now()
			for deadline := time.Now().Add(time.Second); ; {
				rec.mu.Lock()
				got := len(rec.played)
				rec.mu.Unlock()
				if got >= want {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("tick not played")
				}
				time.Sleep(time.Millisecond)
			}
		}
	}
	return p, rec, tick
}

// frames returns n 20 ms frames tagged first, first+1, ...
func frames(first, n int) []int16 {
	pcm := make([]int16, 0, n*320)
	for i := 0; i < n; i++ {
		f := make([]int16, 320)
		f[0] = int16(first + i)
		pcm = append(pcm, f...)
	}
	return pcm
}

func TestLookaheadFeedsBeforePlaying(t *testing.T) {
	p, rec, tick := newAheadHarness(t, 3)
	tick(2) // idle: silence fed and played
	p.Enqueue(frames(10, 4))
	tick(9)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	wantFed := []int16{0, 0, 10, 11, 12, 13, 0, 0, 0, 0, 0}
	wantPlayed := []int16{0, 0, 0, 0, 0, 10, 11, 12, 13, 0, 0}
	if !equalTags(rec.fed, wantFed) || !equalTags(rec.played, wantPlayed) {
		t.Errorf("fed %v\nplayed %v\nwant fed %v\nplayed %v", rec.fed, rec.played, wantFed, wantPlayed)
	}
	// Each frame plays exactly `ahead` ticks after it was fed.
	for i, tag := range rec.fed {
		if tag != 0 && (i+3 >= len(rec.played) || rec.played[i+3] != tag) {
			t.Errorf("frame %d fed at tick %d did not play 3 ticks later", tag, i)
		}
	}
}

func TestLookaheadVideoGoesOutWithItsAudio(t *testing.T) {
	p, rec, tick := newAheadHarness(t, 2)
	// Frame k belongs to fed ticks 2k, 2k+1 and goes out with the audio of
	// tick 2k, which plays `ahead` ticks after it was fed: slot s plays on
	// tick s+2 here.
	p.AttachVideo(0, []byte("v0"))
	p.AttachVideo(1, []byte("v1"))
	tick(6)                        // slots 0..3 played
	p.AttachVideo(2, []byte("v2")) // due with slot 4: on time
	p.AttachVideo(4, []byte("v4")) // due with slot 8
	tick(6)                        // slots 4..9 played
	p.AttachVideo(3, []byte("v3")) // due with slot 6, already played: late
	tick(2)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	got := ""
	for i, v := range rec.video {
		got += fmt.Sprintf("%s@%d ", v, rec.vTick[i])
	}
	if want := "v0@2 v1@4 v2@6 v4@10 v3@12 "; got != want {
		t.Errorf("video %q, want %q", got, want)
	}
}

func TestLookaheadFlushSilencesFedAudio(t *testing.T) {
	p, rec, tick := newAheadHarness(t, 3)
	p.Enqueue(frames(20, 5))
	p.Mark(7)
	tick(4) // 20 played, 21..23 fed
	dropped := p.Flush()
	if len(dropped) != 1 || dropped[0] != 7 {
		t.Errorf("dropped %v", dropped)
	}
	tick(4)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if want := []int16{0, 0, 0, 20, 0, 0, 0, 0}; !equalTags(rec.played, want) {
		t.Errorf("played %v, want %v", rec.played, want)
	}
}

func TestLookaheadMarksTravelWithAudio(t *testing.T) {
	p, _, tick := newAheadHarness(t, 2)
	p.Mark(1) // start mark of a segment
	p.Enqueue(frames(30, 2))
	p.Mark(2) // end mark
	tick(2)
	select {
	case id := <-p.Played():
		t.Fatalf("mark %d before its audio played", id)
	default:
	}
	tick(1) // first frame plays
	if id := recvMark(t, p); id != 1 {
		t.Errorf("first mark %d", id)
	}
	tick(1)
	if id := recvMark(t, p); id != 2 {
		t.Errorf("second mark %d", id)
	}
	p.Mark(3) // nothing queued: due on the next silent slot
	tick(1)
	if id := recvMark(t, p); id != 3 {
		t.Errorf("empty-reply mark %d", id)
	}
}

func TestLookaheadFeedsUngainedAudio(t *testing.T) {
	p, rec, tick := newAheadHarness(t, 1)
	p.SetGain(0)
	pcm := frames(1000, 3)
	p.Enqueue(pcm)
	tick(5)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.fed[0] != 1000 || rec.fed[1] != 1001 {
		t.Errorf("fed %v, want ungained tags", rec.fed[:3])
	}
	if rec.played[1] >= 1000 {
		t.Errorf("played %v: gain should apply to what plays", rec.played)
	}
}

func equalTags(a, b []int16) bool {
	if len(a) < len(b) {
		return false
	}
	for i := range b {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestPlayDelay(t *testing.T) {
	p, _, _ := newAheadHarness(t, 60)
	if d := p.PlayDelay(); d != 1200*time.Millisecond {
		t.Errorf("PlayDelay = %v", d)
	}
	q, err := New(rate, func([]byte, time.Duration) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if q.PlayDelay() != 0 {
		t.Error("delay without a lookahead")
	}
}

func TestStopLookaheadPlaysQueuedAudioAtOnce(t *testing.T) {
	p, rec, tick := newAheadHarness(t, 3)
	p.Enqueue(frames(40, 4))
	tick(2) // 40, 41 fed; nothing played yet
	p.StopLookahead()
	p.AttachVideo(0, []byte("late")) // ignored now
	tick(4)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if want := []int16{0, 0, 40, 41, 42, 43}; !equalTags(rec.played, want) {
		t.Errorf("played %v, want %v", rec.played, want)
	}
	if len(rec.video) != 0 || p.PlayDelay() != 0 {
		t.Errorf("video %d, delay %v after stopping", len(rec.video), p.PlayDelay())
	}
}
