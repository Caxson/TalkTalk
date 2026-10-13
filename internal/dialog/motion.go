package dialog

import (
	"encoding/binary"
	"math"
	"sync/atomic"
	"time"

	"github.com/Caxson/TalkTalk/internal/lipsync"
)

// LipStream turns one segment's speech into blendshape frames.
type LipStream interface {
	Push(pcm []int16) ([]lipsync.Frame, error)
	Flush() ([]lipsync.Frame, error)
}

const (
	// leadAudio: when the player is (nearly) idle, hold this much speech
	// before playing it, so lip-sync frames are ready before the sound.
	leadAudio = 500 * time.Millisecond
	// idleQueue: below this much queued audio there is no head start.
	idleQueue = 300 * time.Millisecond
)

// MotionHeader is the size of the per-frame header: a uint32 sequence.
const MotionHeader = 4

var motionSeq atomic.Uint32

// encodeMotion packs a frame as seq (big-endian uint32) + 52 bytes, each
// weight quantized to 0..255.
func encodeMotion(f lipsync.Frame) []byte {
	b := make([]byte, MotionHeader+lipsync.Channels)
	binary.BigEndian.PutUint32(b, motionSeq.Add(1))
	for i, v := range f {
		b[MotionHeader+i] = byte(math.Round(float64(min(max(v, 0), 1)) * 255))
	}
	return b
}

// voice plays one segment: its audio, its start mark and, when lip-sync is
// on, its motion frames, all only while the turn is current.
type voice struct {
	e    *Engine
	turn int
	seg  spoken
	lips LipStream
	rate int

	started  bool  // start mark placed
	start    int64 // timeline position of the segment's first sample
	frames   int   // motion frames attached so far
	holding  bool
	held     []int16
	detached bool
}

func (e *Engine) newVoice(turn int, seg spoken) *voice {
	v := &voice{e: e, turn: turn, seg: seg, rate: e.d.TTS.SampleRate()}
	if e.d.Lips != nil {
		ls, err := e.d.Lips(v.rate)
		if err != nil {
			e.d.Logger.Warn("lip-sync unavailable for segment", "err", err)
		} else {
			v.lips = ls
			v.holding = e.d.Player.Pending() < idleQueue
		}
	}
	return v
}

// push takes synthesized PCM. It reports whether audio was queued.
func (v *voice) push(pcm []int16) (bool, error) {
	if v.holding {
		v.held = append(v.held, pcm...)
		if time.Duration(len(v.held))*time.Second/time.Duration(v.rate) < leadAudio {
			return false, nil
		}
		pcm, v.held, v.holding = v.held, nil, false
	}
	return v.play(pcm)
}

// finish releases held audio and the segment's remaining motion.
func (v *voice) finish() (bool, error) {
	queued := false
	if len(v.held) > 0 {
		pcm := v.held
		v.held, v.holding = nil, false
		var err error
		if queued, err = v.play(pcm); err != nil {
			return queued, err
		}
	}
	if v.lips != nil && v.started && !v.detached {
		frames, err := v.lips.Flush()
		v.attach(frames, err)
	}
	return queued, nil
}

// play computes motion first, then queues audio and motion together.
func (v *voice) play(pcm []int16) (bool, error) {
	var frames []lipsync.Frame
	var lipErr error
	if v.lips != nil {
		frames, lipErr = v.lips.Push(pcm)
	}
	if !v.started {
		// Mark the segment start right before its first audio so the
		// transcript follows what is actually being heard.
		if !v.e.mark(v.turn, markInfo{turn: v.turn, text: v.seg.text, affect: v.seg.affect}) {
			v.detached = true
			return false, errDetached
		}
		v.started = true
		v.start = v.e.d.Player.EnqueuePos()
	}
	if !v.e.enqueue(v.turn, pcm) {
		v.detached = true
		return false, errDetached
	}
	v.attach(frames, lipErr)
	return true, nil
}

func (v *voice) attach(frames []lipsync.Frame, err error) {
	if err != nil {
		v.e.d.Logger.Warn("lip-sync failed; continuing without motion", "err", err)
		v.lips = nil
		return
	}
	for _, f := range frames {
		pos := v.start + int64(v.frames)*int64(v.rate)/lipsync.FrameRate
		v.frames++
		if !v.e.attachMotion(v.turn, pos, encodeMotion(f)) {
			v.detached = true
			return
		}
	}
}

func (e *Engine) attachMotion(turn int, pos int64, payload []byte) bool {
	e.audioMu.Lock()
	defer e.audioMu.Unlock()
	if e.currentTurn != turn {
		return false
	}
	e.d.Player.AttachMotion(pos, payload)
	return true
}
