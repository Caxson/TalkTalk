package lipsync

import (
	"math"
	"os"
	"testing"

	"github.com/Caxson/TalkTalk/internal/audio"
)

// The model is ~400 MB and not in the repository. Point the tests at it:
//
//	TALKTALK_LIPSYNC_MODEL=/path/to/wav2arkit_cpu.onnx go test ./internal/lipsync
func loadModel(t *testing.T, gain float64) *Model {
	t.Helper()
	path := os.Getenv("TALKTALK_LIPSYNC_MODEL")
	if path == "" {
		t.Skip("TALKTALK_LIPSYNC_MODEL not set")
	}
	m, err := Load(Config{ModelPath: path, LibraryPath: os.Getenv("TALKTALK_ONNXRUNTIME_LIB"), MouthGain: gain})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return m
}

func fixture(t *testing.T) []int16 {
	t.Helper()
	b, err := os.ReadFile("../vad/testdata/speech_16k.pcm")
	if err != nil {
		t.Fatal(err)
	}
	s, err := audio.BytesToInt16(b)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func channel(frames []Frame, name string) []float64 {
	k := -1
	for i, n := range Names {
		if n == name {
			k = i
		}
	}
	out := make([]float64, len(frames))
	for i, f := range frames {
		out[i] = float64(f[k])
	}
	return out
}

func corr(a, b []float64) float64 {
	n := float64(min(len(a), len(b)))
	var sa, sb, sab, sa2, sb2 float64
	for i := 0; i < int(n); i++ {
		sa += a[i]
		sb += b[i]
		sab += a[i] * b[i]
		sa2 += a[i] * a[i]
		sb2 += b[i] * b[i]
	}
	return (sab - sa*sb/n) / math.Sqrt((sa2-sa*sa/n)*(sb2-sb*sb/n)+1e-12)
}

func streamAll(t *testing.T, s *Stream, pcm []int16, chunk int) []Frame {
	t.Helper()
	var out []Frame
	for off := 0; off < len(pcm); off += chunk {
		fr, err := s.Push(pcm[off:min(off+chunk, len(pcm))])
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, fr...)
	}
	tail, err := s.Flush()
	if err != nil {
		t.Fatal(err)
	}
	return append(out, tail...)
}

func TestStreamingMatchesWholeUtterance(t *testing.T) {
	m := loadModel(t, 1)
	pcm := fixture(t)
	whole, err := m.Infer(pcm)
	if err != nil {
		t.Fatal(err)
	}
	s, err := m.NewStream(16000)
	if err != nil {
		t.Fatal(err)
	}
	got := streamAll(t, s, pcm, 1000)

	want := int(math.Ceil(float64(len(pcm)) * FrameRate / 16000))
	if len(got) != want || len(whole) != want {
		t.Fatalf("frames: stream %d, whole %d, want %d", len(got), len(whole), want)
	}
	for _, ch := range []string{"jawOpen", "mouthLowerDownLeft", "mouthPucker"} {
		if c := corr(channel(got, ch), channel(whole, ch)); c < 0.95 {
			t.Errorf("%s: streaming vs whole correlation %.3f, want ≥ 0.95", ch, c)
		}
	}
	var peak float64
	for _, v := range channel(got, "jawOpen") {
		peak = math.Max(peak, v)
	}
	if peak < 0.1 {
		t.Errorf("jawOpen peak %.3f: the mouth barely moves", peak)
	}
}

func TestStreamAt24kMatches16k(t *testing.T) {
	m := loadModel(t, 1)
	pcm16 := fixture(t)
	up, _ := audio.NewResampler(16000, 24000)
	pcm24 := up.Process(pcm16)

	s16, _ := m.NewStream(16000)
	s24, _ := m.NewStream(24000)
	a := streamAll(t, s16, pcm16, 3200)
	b := streamAll(t, s24, pcm24, 4000) // TTS-sized chunks
	if d := len(a) - len(b); d < -1 || d > 1 {
		t.Fatalf("frame counts %d vs %d", len(a), len(b))
	}
	if c := corr(channel(a, "jawOpen"), channel(b, "jawOpen")); c < 0.95 {
		t.Errorf("24 kHz path diverges: correlation %.3f", c)
	}
}

func TestMouthGainScalesOnlyMouthAndClamps(t *testing.T) {
	plain := loadModel(t, 1)
	pcm := fixture(t)
	a, _ := plain.Infer(pcm)
	plain.Close()
	boosted := loadModel(t, 3)
	b, _ := boosted.Infer(pcm)

	ja, jb := channel(a, "jawOpen"), channel(b, "jawOpen")
	for i := range ja {
		if want := math.Min(ja[i]*3, 1); math.Abs(jb[i]-want) > 1e-4 {
			t.Fatalf("jawOpen frame %d: %.4f, want %.4f", i, jb[i], want)
		}
	}
	ba, bb := channel(a, "browInnerUp"), channel(b, "browInnerUp")
	for i := range ba {
		if math.Abs(ba[i]-bb[i]) > 1e-6 {
			t.Fatalf("brow changed by mouth gain at frame %d", i)
		}
	}
}

func TestFlushOnEmptyStream(t *testing.T) {
	m := loadModel(t, 1)
	s, _ := m.NewStream(24000)
	fr, err := s.Flush()
	if err != nil || len(fr) != 0 {
		t.Errorf("Flush on empty stream = %d frames, %v", len(fr), err)
	}
}

func TestLoadErrors(t *testing.T) {
	if _, err := Load(Config{}); err == nil {
		t.Error("Load without a model path succeeded")
	}
	if _, err := Load(Config{ModelPath: "/nonexistent/model.onnx"}); err == nil {
		t.Error("Load of a missing file succeeded")
	}
}

// shiftCorr correlates a with b shifted by lag frames.
func shiftCorr(a, b []float64, lag int) float64 {
	var x, y []float64
	for i := range a {
		if j := i + lag; j >= 0 && j < len(b) {
			x = append(x, a[i])
			y = append(y, b[j])
		}
	}
	return corr(x, y)
}

func TestStreamingIsNotTimeShiftedForAnyChunking(t *testing.T) {
	m := loadModel(t, 1)
	pcm := fixture(t)
	whole, err := m.Infer(pcm)
	if err != nil {
		t.Fatal(err)
	}
	ref := channel(whole, "mouthLowerDownLeft")
	for _, chunk := range []int{7, 479, 1000, 3200} {
		s, _ := m.NewStream(16000)
		got := channel(streamAll(t, s, pcm, chunk), "mouthLowerDownLeft")
		r0 := shiftCorr(got, ref, 0)
		for _, lag := range []int{-1, 1} {
			if r := shiftCorr(got, ref, lag); r > r0 {
				t.Errorf("chunk %d: correlation at lag %+d (%.3f) beats lag 0 (%.3f): frames drift", chunk, lag, r, r0)
			}
		}
	}
}

func TestTinyInputs(t *testing.T) {
	m := loadModel(t, 1)
	for _, n := range []int{1, 100, 320} {
		if fr, err := m.Infer(make([]int16, n)); err != nil || len(fr) != 1 {
			t.Errorf("Infer(%d samples) = %d frames, %v; want 1 frame", n, len(fr), err)
		}
		s, _ := m.NewStream(16000)
		s.Push(make([]int16, n))
		if fr, err := s.Flush(); err != nil || len(fr) != 1 {
			t.Errorf("stream of %d samples: Flush = %d frames, %v", n, len(fr), err)
		}
	}
}
