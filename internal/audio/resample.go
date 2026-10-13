package audio

import (
	"errors"
	"math"
)

// Resampler converts mono PCM between rates by a rational factor (up L,
// down M) with a windowed-sinc polyphase low-pass, streaming: chunks may
// be any size and the output equals resampling the concatenation.
type Resampler struct {
	up, down int
	taps     int         // filter taps per phase
	phases   [][]float64 // phases[p][k]
	hist     []float64   // last taps-1 input samples
	pos      int         // phase accumulator: output index * down, mod up
	identity bool
}

// halfTaps sets filter quality: 2*halfTaps taps per phase. 24 gives a
// stopband below -60 dB, ample for speech.
const halfTaps = 24

// NewResampler returns a resampler from inRate to outRate.
func NewResampler(inRate, outRate int) (*Resampler, error) {
	if inRate <= 0 || outRate <= 0 {
		return nil, errors.New("audio: rates must be positive")
	}
	if inRate == outRate {
		return &Resampler{identity: true}, nil
	}
	g := gcd(inRate, outRate)
	up, down := outRate/g, inRate/g
	taps := 2 * halfTaps
	// Cut-off at the lower Nyquist, relative to the upsampled rate.
	cutoff := 0.5 / float64(max(up, down)) * 0.94
	n := taps * up
	proto := make([]float64, n)
	mid := float64(n-1) / 2
	for i := range proto {
		x := float64(i) - mid
		sinc := 2 * cutoff
		if x != 0 {
			sinc = math.Sin(2*math.Pi*cutoff*x) / (math.Pi * x)
		}
		// Blackman window.
		w := 0.42 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(n-1)) + 0.08*math.Cos(4*math.Pi*float64(i)/float64(n-1))
		proto[i] = sinc * w * float64(up)
	}
	phases := make([][]float64, up)
	for p := range phases {
		phases[p] = make([]float64, taps)
		for k := 0; k < taps; k++ {
			phases[p][k] = proto[k*up+p]
		}
	}
	return &Resampler{up: up, down: down, taps: taps, phases: phases, hist: make([]float64, taps-1)}, nil
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// Process resamples a chunk to int16.
func (r *Resampler) Process(in []int16) []int16 {
	if r.identity {
		return append([]int16(nil), in...)
	}
	out := r.run(in)
	pcm := make([]int16, len(out))
	for i, v := range out {
		pcm[i] = int16(max(-32768, min(32767, math.Round(v))))
	}
	return pcm
}

// ProcessFloat resamples a chunk to float32 in [-1, 1].
func (r *Resampler) ProcessFloat(in []int16) []float32 {
	var out []float64
	if r.identity {
		out = make([]float64, len(in))
		for i, v := range in {
			out[i] = float64(v)
		}
	} else {
		out = r.run(in)
	}
	f := make([]float32, len(out))
	for i, v := range out {
		f[i] = float32(v / 32768)
	}
	return f
}

func (r *Resampler) run(in []int16) []float64 {
	buf := make([]float64, len(r.hist)+len(in))
	copy(buf, r.hist)
	for i, v := range in {
		buf[len(r.hist)+i] = float64(v)
	}
	var out []float64
	// y[m] = Σ_j x[n0-j]·h[p+j·up] with t = m·down, n0 = t/up, p = t%up
	// (the zero-stuffed convolution, keeping only non-zero taps). pos is t
	// relative to the first new sample; buf[n0+k] holds x[n0-(taps-1-k)].
	for {
		n0 := r.pos / r.up
		if n0 >= len(in) {
			break
		}
		ph := r.phases[r.pos%r.up]
		var acc float64
		for k := 0; k < r.taps; k++ {
			acc += buf[n0+k] * ph[r.taps-1-k]
		}
		out = append(out, acc)
		r.pos += r.down
	}
	r.pos -= len(in) * r.up
	copy(r.hist, buf[len(buf)-len(r.hist):])
	return out
}
