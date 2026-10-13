// Package lipsync turns speech audio into ARKit blendshape frames with the
// wav2arkit ONNX model (wav2vec2 + LAM decoder), streaming, on the CPU.
//
// The model is bidirectional: a frame is only accurate once ~0.3 s of the
// audio after it is known. Synthesized speech reaches the server faster
// than real time, so that look-ahead costs nothing here.
package lipsync

import (
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"sync"

	ort "github.com/yalue/onnxruntime_go"

	"github.com/Caxson/TalkTalk/internal/audio"
)

const (
	// Channels is the number of blendshapes per frame.
	Channels = 52
	// FrameRate is the model's output rate.
	FrameRate = 30

	modelRate = 16000
	// Window geometry, in model samples. Window starts snap to a 0.1 s
	// grid so that frame indices line up exactly (0.1 s = 3 frames).
	gridSamples = modelRate / 10
	gridFrames  = 3
	leftContext = modelRate // 1 s of history
	lookAhead   = modelRate * 3 / 10
	hopFrames   = 6 // emit in 0.2 s batches
)

// DefaultLibraryPath is where Homebrew installs onnxruntime.
const DefaultLibraryPath = "/opt/homebrew/opt/onnxruntime/lib/libonnxruntime.dylib"

// Frame holds one 1/30 s of blendshape weights in Names order, 0..1.
type Frame [Channels]float32

// Config for Load.
type Config struct {
	ModelPath   string
	LibraryPath string // onnxruntime shared library; DefaultLibraryPath if empty
	Threads     int    // intra-op threads; 2 if zero
	// MouthGain scales jaw and mouth channels (clamped to 1). The model is
	// trained mostly on English and opens the mouth little for Chinese.
	MouthGain float64
}

var (
	envOnce sync.Once
	envErr  error
)

// Model is a loaded network. Infer is safe for concurrent use.
type Model struct {
	mu      sync.Mutex
	sess    *ort.DynamicAdvancedSession
	in, out string
	gain    [Channels]float32
}

// Load initializes onnxruntime (once per process) and the model.
func Load(cfg Config) (*Model, error) {
	if cfg.ModelPath == "" {
		return nil, errors.New("lipsync: ModelPath is required")
	}
	if _, err := os.Stat(cfg.ModelPath); err != nil {
		return nil, fmt.Errorf("lipsync: model: %w", err)
	}
	if cfg.LibraryPath == "" {
		cfg.LibraryPath = DefaultLibraryPath
	}
	if cfg.Threads <= 0 {
		cfg.Threads = 2
	}
	if cfg.MouthGain <= 0 {
		cfg.MouthGain = 1
	}
	envOnce.Do(func() {
		ort.SetSharedLibraryPath(cfg.LibraryPath)
		envErr = ort.InitializeEnvironment()
	})
	if envErr != nil {
		return nil, fmt.Errorf("lipsync: onnxruntime (%s): %w", cfg.LibraryPath, envErr)
	}
	ins, outs, err := ort.GetInputOutputInfo(cfg.ModelPath)
	if err != nil {
		return nil, fmt.Errorf("lipsync: inspect model: %w", err)
	}
	if len(ins) != 1 || len(outs) != 1 {
		return nil, fmt.Errorf("lipsync: unexpected model signature %v -> %v", ins, outs)
	}
	opts, err := ort.NewSessionOptions()
	if err != nil {
		return nil, fmt.Errorf("lipsync: session options: %w", err)
	}
	defer opts.Destroy()
	if err := opts.SetIntraOpNumThreads(cfg.Threads); err != nil {
		return nil, fmt.Errorf("lipsync: threads: %w", err)
	}
	sess, err := ort.NewDynamicAdvancedSession(cfg.ModelPath, []string{ins[0].Name}, []string{outs[0].Name}, opts)
	if err != nil {
		return nil, fmt.Errorf("lipsync: load model: %w", err)
	}
	m := &Model{sess: sess, in: ins[0].Name, out: outs[0].Name}
	for i, n := range Names {
		m.gain[i] = 1
		if strings.HasPrefix(n, "jaw") || strings.HasPrefix(n, "mouth") {
			m.gain[i] = float32(cfg.MouthGain)
		}
	}
	return m, nil
}

// Close releases the session.
func (m *Model) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sess == nil {
		return nil
	}
	err := m.sess.Destroy()
	m.sess = nil
	return err
}

// Infer runs one whole utterance at 16 kHz.
func (m *Model) Infer(pcm16k []int16) ([]Frame, error) {
	if len(pcm16k) == 0 {
		return nil, nil
	}
	x := make([]float32, gridCeil(len(pcm16k)))
	for i, v := range pcm16k {
		x[i] = float32(v) / 32768
	}
	frames, err := m.run(x)
	if err != nil {
		return nil, err
	}
	return frames[:min(len(frames), framesFor(len(pcm16k)))], nil
}

// The model resamples its internal 50 Hz features to 30 fps over the
// whole window, so a frame's position in time depends on the window's
// length. Windows therefore always span whole 0.1 s grid cells (exactly
// 3 frames each); partial cells are zero-padded.
func gridCeil(n int) int  { return (n + gridSamples - 1) / gridSamples * gridSamples }
func gridFloor(n int) int { return n / gridSamples * gridSamples }

// framesFor is the frame count covering n model-rate samples.
func framesFor(n int) int { return (n*FrameRate + modelRate - 1) / modelRate }

func (m *Model) run(x []float32) ([]Frame, error) {
	if len(x) == 0 {
		return nil, nil
	}
	in, err := ort.NewTensor(ort.NewShape(1, int64(len(x))), x)
	if err != nil {
		return nil, fmt.Errorf("lipsync: input tensor: %w", err)
	}
	defer in.Destroy()
	outs := []ort.Value{nil}

	m.mu.Lock()
	if m.sess == nil {
		m.mu.Unlock()
		return nil, errors.New("lipsync: model closed")
	}
	err = m.sess.Run([]ort.Value{in}, outs)
	m.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("lipsync: run: %w", err)
	}
	defer outs[0].Destroy()

	t, ok := outs[0].(*ort.Tensor[float32])
	if !ok {
		return nil, errors.New("lipsync: unexpected output type")
	}
	shape, data := t.GetShape(), t.GetData()
	if len(shape) != 3 || shape[2] != Channels {
		return nil, fmt.Errorf("lipsync: unexpected output shape %v", shape)
	}
	frames := make([]Frame, shape[1])
	for i := range frames {
		for k := 0; k < Channels; k++ {
			v := data[i*Channels+k] * m.gain[k]
			frames[i][k] = float32(math.Min(math.Max(float64(v), 0), 1))
		}
	}
	return frames, nil
}

// Stream produces frames for one continuous piece of speech as its audio
// arrives. Frame k covers [k/30, (k+1)/30) s from the first pushed sample.
// Not safe for concurrent use.
type Stream struct {
	m    *Model
	rs   *audio.Resampler
	buf  []float32 // model-rate audio from sample base onwards
	base int
	done int // frames emitted so far
}

// NewStream starts a stream for PCM at inRate.
func (m *Model) NewStream(inRate int) (*Stream, error) {
	rs, err := audio.NewResampler(inRate, modelRate)
	if err != nil {
		return nil, err
	}
	return &Stream{m: m, rs: rs}, nil
}

// Push adds audio and returns the frames whose look-ahead is now complete.
func (s *Stream) Push(pcm []int16) ([]Frame, error) {
	s.buf = append(s.buf, s.rs.ProcessFloat(pcm)...)
	end := gridFloor(s.base + len(s.buf))
	// Frame k is final once audio reaches its end plus the look-ahead.
	ready := max(end-lookAhead, 0) * FrameRate / modelRate
	if ready-s.done < hopFrames {
		return nil, nil
	}
	return s.emit(ready, end)
}

// Flush returns the remaining frames, using whatever audio there is.
func (s *Stream) Flush() ([]Frame, error) {
	end := s.base + len(s.buf)
	total := framesFor(end)
	if total <= s.done {
		return nil, nil
	}
	padded := gridCeil(end)
	s.buf = append(s.buf, make([]float32, padded-end)...)
	return s.emit(total, padded)
}

// emit infers frames [done, upto) over the grid-aligned window [lo, end)
// that includes left context.
func (s *Stream) emit(upto, end int) ([]Frame, error) {
	firstSample := s.done * modelRate / FrameRate
	lo := max(gridFloor(max(firstSample-leftContext, 0)), s.base)
	frames, err := s.m.run(s.buf[lo-s.base : end-s.base])
	if err != nil {
		return nil, err
	}
	offset := lo / gridSamples * gridFrames // frame index of frames[0]
	from, to := s.done-offset, min(upto-offset, len(frames))
	if from < 0 || from > to {
		return nil, fmt.Errorf("lipsync: window misaligned (from %d, to %d)", from, to)
	}
	out := append([]Frame(nil), frames[from:to]...)
	s.done = offset + to

	// Drop audio no future window will need.
	keepFrom := gridFloor(max(s.done*modelRate/FrameRate-leftContext, 0))
	if keepFrom > s.base {
		s.buf = append([]float32(nil), s.buf[keepFrom-s.base:]...)
		s.base = keepFrom
	}
	return out, nil
}
