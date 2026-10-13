package lipsync

import (
	"math"
	"testing"
)

// TestNaturalLead measures how far the model's jaw leads the audio
// envelope on the same audio, with no transport in between. It is a
// measurement (logged), not an assertion of a target.
func TestNaturalLead(t *testing.T) {
	m := loadModel(t, 1)
	pcm := fixture(t)
	frames, err := m.Infer(pcm)
	if err != nil {
		t.Fatal(err)
	}
	// Both curves on a 10 ms grid.
	n := len(pcm) / 160
	env := make([]float64, n)
	for i := range env {
		var s float64
		for _, v := range pcm[i*160 : (i+1)*160] {
			s += float64(v) * float64(v)
		}
		env[i] = math.Sqrt(s / 160)
	}
	jawCh := channel(frames, "jawOpen")
	jaw := make([]float64, n)
	for i := range jaw {
		x := float64(i) * 10 / (1000.0 / FrameRate) // frame position
		k := int(x)
		if k+1 >= len(jawCh) {
			jaw[i] = jawCh[len(jawCh)-1]
			continue
		}
		jaw[i] = jawCh[k] + (jawCh[k+1]-jawCh[k])*(x-float64(k))
	}
	bestLag, bestR := 0, -2.0
	for lag := -30; lag <= 30; lag++ { // ±300 ms; positive = jaw after sound
		var a, b []float64
		for i := range env {
			if j := i + lag; j >= 0 && j < n {
				a = append(a, env[i])
				b = append(b, jaw[j])
			}
		}
		if r := corr(a, b); r > bestR {
			bestR, bestLag = r, lag
		}
	}
	t.Logf("model alone: audio→jaw peak r=%.2f at lag %+d ms", bestR, bestLag*10)
}
