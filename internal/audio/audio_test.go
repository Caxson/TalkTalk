package audio

import (
	"math"
	"testing"
	"time"
)

func sine(rate, ms int, freq, amp float64) []int16 {
	n := rate * ms / 1000
	out := make([]int16, n)
	for i := range out {
		out[i] = int16(amp * math.Sin(2*math.Pi*freq*float64(i)/float64(rate)))
	}
	return out
}

func TestBytesRoundTrip(t *testing.T) {
	in := []int16{0, 1, -1, 32767, -32768, 1234}
	b := Int16ToBytes(in)
	if len(b) != 2*len(in) || b[2] != 0x01 || b[3] != 0x00 {
		t.Fatalf("not little-endian: % x", b)
	}
	out, err := BytesToInt16(b)
	if err != nil {
		t.Fatal(err)
	}
	for i := range in {
		if in[i] != out[i] {
			t.Fatalf("round trip mismatch at %d: %d != %d", i, in[i], out[i])
		}
	}
	if _, err := BytesToInt16([]byte{1, 2, 3}); err == nil {
		t.Error("odd byte count must error")
	}
}

func TestRMSdBFS(t *testing.T) {
	if got := RMSdBFS(make([]int16, 160)); !math.IsInf(got, -1) {
		t.Errorf("silence = %v, want -Inf", got)
	}
	full := sine(16000, 100, 440, 32767)
	if got := RMSdBFS(full); math.Abs(got-(-3.01)) > 0.1 {
		t.Errorf("full-scale sine = %.2f dBFS, want about -3.01", got)
	}
	if got := RMSdBFS(nil); !math.IsInf(got, -1) {
		t.Errorf("empty = %v, want -Inf", got)
	}
}

func TestFramerEmitsFixedFrames(t *testing.T) {
	f := NewFramer(16000, 20*time.Millisecond) // 320 samples
	var frames [][]int16
	frames = append(frames, f.Push(make([]int16, 100))...)
	if len(frames) != 0 {
		t.Fatalf("emitted %d frames from 100 samples", len(frames))
	}
	frames = append(frames, f.Push(make([]int16, 700))...)
	if len(frames) != 2 {
		t.Fatalf("got %d frames from 800 samples, want 2", len(frames))
	}
	for _, fr := range frames {
		if len(fr) != 320 {
			t.Errorf("frame len %d, want 320", len(fr))
		}
	}
	last, ok := f.Flush()
	if !ok || len(last) != 320 {
		t.Fatalf("Flush = %d samples, %v; want padded 320", len(last), ok)
	}
	if _, ok := f.Flush(); ok {
		t.Error("second Flush must be empty")
	}
}

func TestFramerFramesAreIndependentCopies(t *testing.T) {
	f := NewFramer(16000, 20*time.Millisecond)
	in := make([]int16, 640)
	for i := range in {
		in[i] = int16(i)
	}
	frames := f.Push(in)
	in[0] = 9999
	frames[0][1] = -1
	if frames[0][0] != 0 || frames[1][0] != 320 {
		t.Errorf("frames alias caller or each other: %v %v", frames[0][0], frames[1][0])
	}
}

func TestFramerDrainIsUnpadded(t *testing.T) {
	f := NewFramer(16000, 20*time.Millisecond)
	f.Push(make([]int16, 330))
	if got := f.Drain(); len(got) != 10 {
		t.Errorf("Drain = %d samples, want 10", len(got))
	}
	if got := f.Drain(); got != nil {
		t.Errorf("second Drain = %v, want nil", got)
	}
}

func TestFramerReset(t *testing.T) {
	f := NewFramer(16000, 20*time.Millisecond)
	f.Push(make([]int16, 100))
	f.Reset()
	if _, ok := f.Flush(); ok {
		t.Error("Reset must drop buffered samples")
	}
}

func TestOpusRoundTripAcrossRates(t *testing.T) {
	// Encode at the TTS rate, decode at the ASR rate: the path audio takes
	// when TalkTalk hears its own voice in loopback tests.
	enc, err := NewOpusEncoder(24000)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := NewOpusDecoder(16000)
	if err != nil {
		t.Fatal(err)
	}
	if enc.FrameSamples() != 480 {
		t.Fatalf("FrameSamples = %d, want 480 (20ms @ 24k)", enc.FrameSamples())
	}

	src := sine(24000, 400, 440, 12000)
	var decoded []int16
	for i := 0; i+480 <= len(src); i += 480 {
		pkt, err := enc.Encode(src[i : i+480])
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		pcm, err := dec.Decode(pkt)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(pcm) != 320 {
			t.Fatalf("decoded %d samples, want 320 (20ms @ 16k)", len(pcm))
		}
		decoded = append(decoded, pcm...)
	}
	// Skip codec warm-up, then the level should match the source closely.
	in, out := RMSdBFS(src[4800:]), RMSdBFS(decoded[3200:])
	if math.Abs(in-out) > 2 {
		t.Errorf("level changed through codec: in %.1f dBFS, out %.1f dBFS", in, out)
	}
}

func TestOpusEncoderRejectsWrongFrameSize(t *testing.T) {
	enc, err := NewOpusEncoder(48000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := enc.Encode(make([]int16, 100)); err == nil {
		t.Error("want error for non-20ms frame")
	}
}

func TestOpusRejectsUnsupportedRate(t *testing.T) {
	if _, err := NewOpusEncoder(44100); err == nil {
		t.Error("encoder accepted 44100")
	}
	if _, err := NewOpusDecoder(22050); err == nil {
		t.Error("decoder accepted 22050")
	}
}

func TestOpusDecodeGarbage(t *testing.T) {
	dec, err := NewOpusDecoder(16000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dec.Decode(nil); err == nil {
		t.Error("want error decoding empty packet")
	}
}
