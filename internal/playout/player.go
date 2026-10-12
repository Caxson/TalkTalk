// Package playout paces synthesized speech onto a real-time audio track.
//
// A Player ticks every 20ms and always emits exactly one Opus frame: queued
// speech when there is some, otherwise silence, so RTP timestamps advance
// with wall time and the receiver's jitter buffer never sees gaps.
//
// Marks placed between audio chunks report how far playback has actually
// progressed, which lets the dialog know which sentences the user heard
// before a barge-in.
package playout

import (
	"context"
	"fmt"
	"time"

	"sync"

	"github.com/Caxson/TalkTalk/internal/audio"
)

// Sink receives one encoded frame and its duration (e.g. a WebRTC track).
type Sink func(packet []byte, duration time.Duration) error

// item is either a full audio frame or a mark (frame == nil).
type item struct {
	frame []int16
	mark  int
}

// Player is safe for concurrent use: producers call Enqueue/Mark/Flush while
// Run drives playback.
type Player struct {
	enc     *audio.OpusEncoder
	encode  func([]int16) ([]byte, error) // enc.Encode; swappable in tests
	sink    Sink
	silence []int16
	played  chan int

	mu      sync.Mutex
	framer  *audio.Framer
	queue   []item
	pending int // queued audio frames

	gain       float64 // applied to the frame being played
	targetGain float64
}

// gainStep bounds the gain change per 20ms frame, so ducking ramps over a
// few frames instead of clicking.
const gainStep = 0.25

// New returns a Player for mono PCM at sampleRate.
func New(sampleRate int, sink Sink) (*Player, error) {
	enc, err := audio.NewOpusEncoder(sampleRate)
	if err != nil {
		return nil, err
	}
	p := &Player{
		enc:        enc,
		sink:       sink,
		silence:    make([]int16, enc.FrameSamples()),
		played:     make(chan int, 256),
		framer:     audio.NewFramer(sampleRate, audio.FrameDuration),
		gain:       1,
		targetGain: 1,
	}
	p.encode = enc.Encode
	return p, nil
}

// SetGain ramps playback volume toward g (0..1), e.g. to duck speech while
// the user may be starting to talk.
func (p *Player) SetGain(g float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.targetGain = min(max(g, 0), 1)
}

// Played delivers mark ids in playback order.
func (p *Player) Played() <-chan int { return p.played }

// Enqueue appends speech. Samples are copied.
func (p *Player) Enqueue(pcm []int16) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, f := range p.framer.Push(pcm) {
		p.queue = append(p.queue, item{frame: f})
		p.pending++
	}
}

// Mark places id after all audio enqueued so far. Any partial frame is
// padded so the mark lands exactly at the end of that audio.
func (p *Player) Mark(id int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if f, ok := p.framer.Flush(); ok {
		p.queue = append(p.queue, item{frame: f})
		p.pending++
	}
	p.queue = append(p.queue, item{mark: id})
}

// Flush discards all queued audio and returns the ids of marks that will now
// never play, in order.
func (p *Player) Flush() []int {
	p.mu.Lock()
	defer p.mu.Unlock()
	var dropped []int
	for _, it := range p.queue {
		if it.frame == nil {
			dropped = append(dropped, it.mark)
		}
	}
	p.queue, p.pending = nil, 0
	p.framer.Reset()
	// Whatever plays next starts at full volume.
	p.gain, p.targetGain = 1, 1
	return dropped
}

// Pending is the duration of speech still queued.
func (p *Player) Pending() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	return time.Duration(p.pending) * audio.FrameDuration
}

// Run plays until ctx is done or the sink fails.
func (p *Player) Run(ctx context.Context) error {
	t := time.NewTicker(audio.FrameDuration)
	defer t.Stop()
	return p.run(ctx, t.C)
}

func (p *Player) run(ctx context.Context, ticks <-chan time.Time) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticks:
		}
		frame, marks := p.next()
		for _, id := range marks {
			p.emit(id)
		}
		pkt, err := p.encode(frame)
		if err != nil {
			return err
		}
		if err := p.sink(pkt, audio.FrameDuration); err != nil {
			return fmt.Errorf("playout: sink: %w", err)
		}
	}
}

// next pops marks that are due plus at most one frame. Marks after the
// popped frame are emitted on the following tick, once that frame has been
// handed to the sink.
func (p *Player) next() ([]int16, []int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var marks []int
	for len(p.queue) > 0 && p.queue[0].frame == nil {
		marks = append(marks, p.queue[0].mark)
		p.queue = p.queue[1:]
	}
	if len(p.queue) == 0 {
		p.queue = nil
		return p.silence, marks
	}
	f := p.applyGain(p.queue[0].frame)
	p.queue = p.queue[1:]
	p.pending--
	// A mark right behind the last frame of a segment is due as soon as
	// that frame is sent; emit it now rather than a tick late.
	for len(p.queue) > 0 && p.queue[0].frame == nil {
		marks = append(marks, p.queue[0].mark)
		p.queue = p.queue[1:]
	}
	return f, marks
}

// applyGain steps the gain toward its target and scales f in place (queued
// frames are private copies). Called with mu held.
func (p *Player) applyGain(f []int16) []int16 {
	from := p.gain
	switch {
	case p.gain < p.targetGain:
		p.gain = min(p.gain+gainStep, p.targetGain)
	case p.gain > p.targetGain:
		p.gain = max(p.gain-gainStep, p.targetGain)
	}
	if from == 1 && p.gain == 1 {
		return f
	}
	// Interpolate across the frame so the step itself is inaudible.
	n := float64(len(f))
	for i, v := range f {
		g := from + (p.gain-from)*float64(i+1)/n
		f[i] = int16(float64(v) * g)
	}
	return f
}

func (p *Player) emit(id int) {
	select {
	case p.played <- id:
	default:
		// Consumer is gone or badly behind; dropping a progress mark is
		// preferable to stalling real-time playout.
	}
}
