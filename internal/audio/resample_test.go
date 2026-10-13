package audio

import (
	"math"
	"testing"
)

func tone(rate, n int, freq, amp float64) []int16 {
	out := make([]int16, n)
	for i := range out {
		out[i] = int16(amp * math.Sin(2*math.Pi*freq*float64(i)/float64(rate)))
	}
	return out
}

// zeroCrossings estimates frequency robustly enough for a pure tone.
func zeroCrossings(s []int16) int {
	n := 0
	for i := 1; i < len(s); i++ {
		if (s[i-1] < 0) != (s[i] < 0) {
			n++
		}
	}
	return n
}

func TestResamplePreservesPassband(t *testing.T) {
	r, err := NewResampler(24000, 16000)
	if err != nil {
		t.Fatal(err)
	}
	in := tone(24000, 24000, 1000, 10000) // 1s of 1 kHz
	out := r.Process(in)
	if len(out) < 15900 || len(out) > 16000 {
		t.Fatalf("output length %d, want ≈16000", len(out))
	}
	body := out[800 : len(out)-800] // skip filter warm-up
	if d := RMSdBFS(body) - RMSdBFS(in); math.Abs(d) > 0.5 {
		t.Errorf("passband level changed by %.2f dB", d)
	}
	// 1 kHz over len(body)/16000 seconds: two crossings per cycle.
	want := 2 * 1000 * float64(len(body)) / 16000
	if got := float64(zeroCrossings(body)); math.Abs(got-want) > 4 {
		t.Errorf("zero crossings %v, want ≈%v (frequency shifted)", got, want)
	}
}

func TestResampleRejectsAliases(t *testing.T) {
	r, _ := NewResampler(24000, 16000)
	in := tone(24000, 24000, 10000, 10000) // above the 8 kHz output Nyquist
	out := r.Process(in)
	if d := RMSdBFS(out[800:len(out)-800]) - RMSdBFS(in); d > -40 {
		t.Errorf("10 kHz tone attenuated only %.1f dB, want ≤ -40", d)
	}
}

func TestResampleStreamingMatchesOneShot(t *testing.T) {
	in := tone(24000, 12000, 440, 8000)
	whole, _ := NewResampler(24000, 16000)
	want := whole.Process(in)

	chunked, _ := NewResampler(24000, 16000)
	var got []int16
	for off := 0; off < len(in); off += 333 {
		got = append(got, chunked.Process(in[off:min(off+333, len(in))])...)
	}
	if len(got) != len(want) {
		t.Fatalf("chunked length %d, one-shot %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sample %d differs: %d vs %d", i, got[i], want[i])
		}
	}
}

func TestResampleIdentityAndErrors(t *testing.T) {
	r, err := NewResampler(16000, 16000)
	if err != nil {
		t.Fatal(err)
	}
	in := []int16{1, 2, 3}
	if out := r.Process(in); len(out) != 3 || out[2] != 3 {
		t.Errorf("identity resample = %v", out)
	}
	if _, err := NewResampler(0, 16000); err == nil {
		t.Error("accepted zero rate")
	}
}

func TestResampleFloat(t *testing.T) {
	r, _ := NewResampler(24000, 16000)
	f := r.ProcessFloat(tone(24000, 2400, 1000, 16384))
	var peak float64
	for _, v := range f[200:] {
		peak = math.Max(peak, math.Abs(float64(v)))
	}
	if peak < 0.45 || peak > 0.55 {
		t.Errorf("float peak %.3f, want ≈0.5 (scaled to [-1,1])", peak)
	}
}

// snrAgainstIdeal fits the best delay and returns the SNR (dB) of out
// against a pure tone at outRate.
func snrAgainstIdeal(out []int16, outRate int, freq, amp float64) float64 {
	best := -1e9
	for d := 0.0; d < 80; d += 0.1 { // delay in output samples
		var sig, errp float64
		for i := 400; i < len(out)-400; i++ {
			ideal := amp * math.Sin(2*math.Pi*freq*(float64(i)-d)/float64(outRate))
			e := float64(out[i]) - ideal
			sig += ideal * ideal
			errp += e * e
		}
		best = math.Max(best, 10*math.Log10(sig/(errp+1e-9)))
	}
	return best
}

func TestResampleIsCleanNotJustRightLevel(t *testing.T) {
	cases := []struct{ in, out int }{{24000, 16000}, {16000, 24000}, {48000, 16000}}
	for _, c := range cases {
		r, _ := NewResampler(c.in, c.out)
		out := r.Process(tone(c.in, c.in/2, 700, 10000))
		if snr := snrAgainstIdeal(out, c.out, 700, 10000); snr < 40 {
			t.Errorf("%d→%d: SNR %.1f dB against an ideal tone, want ≥ 40", c.in, c.out, snr)
		}
	}
}
