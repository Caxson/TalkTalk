// Package vad detects the onset and end of speech in a live, echo-cancelled
// microphone feed, fast enough to let the assistant yield before the
// recognizer has produced any text.
//
// It is an energy detector with an adaptive noise floor. False positives
// are acceptable by design: callers react softly (duck, not stop) and wait
// for recognized words before committing to an interruption.
package vad

import (
	"math"
	"sync/atomic"
	"time"

	"github.com/Caxson/TalkTalk/internal/audio"
)

// Kind of transition.
type Kind int

const (
	Onset  Kind = iota + 1 // speech began
	Offset                 // speech ended
)

// Transition is a change between silence and speech. Positions are sample
// offsets since the detector started: StartedAt estimates when the change
// really happened, DetectedAt is when it became certain.
type Transition struct {
	Kind       Kind
	StartedAt  int64
	DetectedAt int64
}

// Config tunes the detector. Zero values get defaults.
type Config struct {
	SampleRate int
	// OnsetMarginDB: how far above the noise floor a frame must be to
	// count as speech.
	OnsetMarginDB float64
	// OffsetMarginDB: below floor+this, a frame counts as silence.
	OffsetMarginDB float64
	// PlaybackExtraDB raises both margins while the assistant is talking,
	// so residual echo the browser failed to cancel does not count.
	PlaybackExtraDB float64
	// MinSpeechDBFS rejects anything quieter regardless of the floor.
	MinSpeechDBFS float64
	// OnsetWindow is judged by majority: speech has gaps between syllables.
	OnsetWindow time.Duration
	Hangover    time.Duration
	// FloorRiseDBPerSec lets the floor follow lasting noise changes, even
	// mid-"speech", so a fan switching on cannot pin the detector open.
	FloorRiseDBPerSec float64
}

func (c Config) withDefaults() Config {
	def := func(v *float64, d float64) {
		if *v == 0 {
			*v = d
		}
	}
	def(&c.OnsetMarginDB, 12)
	def(&c.OffsetMarginDB, 6)
	def(&c.PlaybackExtraDB, 6)
	def(&c.MinSpeechDBFS, -55)
	def(&c.FloorRiseDBPerSec, 1.5)
	if c.OnsetWindow == 0 {
		c.OnsetWindow = 160 * time.Millisecond
	}
	if c.Hangover == 0 {
		c.Hangover = 300 * time.Millisecond
	}
	if c.SampleRate == 0 {
		c.SampleRate = 16000
	}
	return c
}

const (
	frameDur   = 20 * time.Millisecond
	floorLimit = -80.0 // digital silence must not drag the floor to -Inf
	fallRate   = 0.5   // fraction of the gap closed per quieter frame

	// Speech is anything but steady: syllables swing the level by 10 dB or
	// more frame to frame. A "speech" run whose level barely moves for
	// stationaryWindow is noise (a fan, or room noise after the stream
	// opened on digital zeros), and the floor catches up quickly.
	stationaryWindow = time.Second
	stationaryStdDB  = 3.0
	fastRiseDBPerSec = 10.0

	// recentVoice: Sustained requires a loud frame this recently, so the
	// hangover's trailing silence never counts as talking.
	recentVoice = 100 * time.Millisecond
)

// Detector is fed from one goroutine; SetPlayback may be called from any.
type Detector struct {
	cfg       Config
	framer    *audio.Framer
	frameLen  int64
	riseStep  float64
	window    []bool // ring of recent "loud" verdicts
	need      int
	hangFrame int

	playback atomic.Bool
	speaking atomic.Bool
	// Sample positions, published for Sustained (read from other goroutines).
	pos       atomic.Int64 // samples consumed so far (frame-aligned)
	onsetAt   atomic.Int64 // start of the current speech run
	lastLoud  atomic.Int64 // end of the latest loud frame
	recentLen int64

	levels   []float64 // ring of recent frame levels, for stationarity
	lvHead   int
	lvCount  int
	fastStep float64

	floor    float64
	started  bool
	head     int
	loud     int
	quietRun int
}

// New returns a Detector.
func New(cfg Config) *Detector {
	cfg = cfg.withDefaults()
	n := max(int(cfg.OnsetWindow/frameDur), 1)
	frameLen := int64(cfg.SampleRate) * int64(frameDur) / int64(time.Second)
	return &Detector{
		cfg:       cfg,
		framer:    audio.NewFramer(cfg.SampleRate, frameDur),
		frameLen:  frameLen,
		riseStep:  cfg.FloorRiseDBPerSec * frameDur.Seconds(),
		window:    make([]bool, n),
		need:      max((3*n+3)/4, 1),
		hangFrame: max(int(cfg.Hangover/frameDur), 1),
		recentLen: frameLen * int64(recentVoice/frameDur),
		levels:    make([]float64, int(stationaryWindow/frameDur)),
		fastStep:  fastRiseDBPerSec * frameDur.Seconds(),
	}
}

// Sustained is how long the user has been talking without pause, or 0 if
// they are not audibly talking right now (silence inside the hangover does
// not count). Safe to call from any goroutine.
func (d *Detector) Sustained() time.Duration {
	if !d.speaking.Load() {
		return 0
	}
	now := d.pos.Load()
	if now-d.lastLoud.Load() > d.recentLen {
		return 0
	}
	return time.Duration(now-d.onsetAt.Load()) * time.Second / time.Duration(d.cfg.SampleRate)
}

// SetPlayback tells the detector whether the assistant is audible.
func (d *Detector) SetPlayback(on bool) { d.playback.Store(on) }

// Speaking reports the current verdict.
func (d *Detector) Speaking() bool { return d.speaking.Load() }

// Process consumes PCM of any length and returns transitions, in order.
func (d *Detector) Process(pcm []int16) []Transition {
	var out []Transition
	for _, f := range d.framer.Push(pcm) {
		if t, ok := d.frame(f); ok {
			out = append(out, t)
		}
		d.pos.Add(d.frameLen)
	}
	return out
}

func (d *Detector) frame(f []int16) (Transition, bool) {
	level := audio.RMSdBFS(f)
	if math.IsInf(level, -1) || level < floorLimit {
		level = floorLimit
	}
	if !d.started {
		// Assume the stream opens on background noise, not mid-word.
		d.floor, d.started = level, true
	}

	extra := 0.0
	if d.playback.Load() {
		extra = d.cfg.PlaybackExtraDB
	}
	loud := level >= d.floor+d.cfg.OnsetMarginDB+extra && level >= d.cfg.MinSpeechDBFS
	quiet := level < d.floor+d.cfg.OffsetMarginDB+extra

	pos := d.pos.Load()
	end := pos + d.frameLen
	d.trackFloor(level)
	d.push(loud)
	if loud {
		d.lastLoud.Store(end)
	}

	if !d.speaking.Load() {
		if d.loud >= d.need {
			// The window opened at most len(window) frames ago.
			start := max(end-int64(len(d.window))*d.frameLen, 0)
			d.onsetAt.Store(start)
			d.speaking.Store(true)
			d.quietRun = 0
			return Transition{Kind: Onset, StartedAt: start, DetectedAt: end}, true
		}
		return Transition{}, false
	}

	if quiet {
		d.quietRun++
	} else {
		d.quietRun = 0
	}
	if d.quietRun >= d.hangFrame {
		d.speaking.Store(false)
		d.clearWindow()
		start := end - int64(d.quietRun)*d.frameLen
		d.quietRun = 0
		return Transition{Kind: Offset, StartedAt: start, DetectedAt: end}, true
	}
	return Transition{}, false
}

// trackFloor follows quieter levels quickly and louder ones slowly, unless
// a supposed speech run is as steady as noise.
func (d *Detector) trackFloor(level float64) {
	d.levels[d.lvHead] = level
	d.lvHead = (d.lvHead + 1) % len(d.levels)
	d.lvCount = min(d.lvCount+1, len(d.levels))

	switch {
	case level < d.floor:
		d.floor += (level - d.floor) * fallRate
	case d.speaking.Load() && d.stationary():
		d.floor += min(d.fastStep, level-d.floor)
	default:
		d.floor += min(d.riseStep, level-d.floor)
	}
	d.floor = max(d.floor, floorLimit)
}

// stationary reports whether the last stationaryWindow of levels is flat.
func (d *Detector) stationary() bool {
	if d.lvCount < len(d.levels) {
		return false
	}
	var sum, sq float64
	for _, v := range d.levels {
		sum += v
		sq += v * v
	}
	n := float64(len(d.levels))
	mean := sum / n
	return math.Sqrt(max(sq/n-mean*mean, 0)) < stationaryStdDB
}

func (d *Detector) push(loud bool) {
	if d.window[d.head] {
		d.loud--
	}
	d.window[d.head] = loud
	if loud {
		d.loud++
	}
	d.head = (d.head + 1) % len(d.window)
}

func (d *Detector) clearWindow() {
	for i := range d.window {
		d.window[i] = false
	}
	d.loud = 0
}
