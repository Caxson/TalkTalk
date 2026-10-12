package vad

import (
	"math"
	"math/rand"
	"os"
	"testing"
	"time"

	"github.com/Caxson/TalkTalk/internal/audio"
)

const rate = 16000

func speech(t *testing.T) []int16 {
	t.Helper()
	b, err := os.ReadFile("testdata/speech_16k.pcm")
	if err != nil {
		t.Fatal(err)
	}
	s, err := audio.BytesToInt16(b)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// scaleTo returns s scaled so its RMS is dbfs.
func scaleTo(s []int16, dbfs float64) []int16 {
	g := math.Pow(10, (dbfs-audio.RMSdBFS(s))/20)
	out := make([]int16, len(s))
	for i, v := range s {
		out[i] = clamp(float64(v) * g)
	}
	return out
}

func noise(n int, dbfs float64, seed int64) []int16 {
	r := rand.New(rand.NewSource(seed))
	amp := math.Pow(10, dbfs/20) * 32768 // RMS of N(0,1)*amp is amp
	out := make([]int16, n)
	for i := range out {
		out[i] = clamp(r.NormFloat64() * amp)
	}
	return out
}

func mix(a, b []int16) []int16 {
	out := make([]int16, max(len(a), len(b)))
	for i := range out {
		var v float64
		if i < len(a) {
			v += float64(a[i])
		}
		if i < len(b) {
			v += float64(b[i])
		}
		out[i] = clamp(v)
	}
	return out
}

func clamp(v float64) int16 { return int16(max(-32768, min(32767, v))) }

func seconds(d float64) int { return int(d * rate) }

// run feeds pcm in 20ms chunks, like the WebRTC leg, and returns all
// transitions.
func run(d *Detector, pcm []int16) []Transition {
	var out []Transition
	for off := 0; off < len(pcm); off += 320 {
		out = append(out, d.Process(pcm[off:min(off+320, len(pcm))])...)
	}
	return out
}

func newDetector() *Detector { return New(Config{SampleRate: rate}) }

func onsets(ts []Transition) []Transition {
	var out []Transition
	for _, t := range ts {
		if t.Kind == Onset {
			out = append(out, t)
		}
	}
	return out
}

func TestStationaryNoiseNeverTriggers(t *testing.T) {
	for _, lvl := range []float64{-65, -50, -35} {
		d := newDetector()
		if got := onsets(run(d, noise(seconds(10), lvl, 1))); len(got) != 0 {
			t.Errorf("%v dBFS noise: %d onsets", lvl, len(got))
		}
	}
}

func TestSpeechTriggersQuicklyAcrossNoiseLevels(t *testing.T) {
	sp := speech(t)
	cases := []struct{ noiseDB, speechDB float64 }{
		{-65, -26}, // quiet room
		{-50, -26}, // ordinary room
		{-40, -22}, // noisy: 18 dB SNR
	}
	for _, c := range cases {
		d := newDetector()
		lead := seconds(2)
		pcm := mix(noise(lead+len(sp)+seconds(2), c.noiseDB, 2), append(make([]int16, lead), scaleTo(sp, c.speechDB)...))
		ts := run(d, pcm)
		on := onsets(ts)
		if len(on) == 0 {
			t.Errorf("noise %v / speech %v: no onset", c.noiseDB, c.speechDB)
			continue
		}
		delay := time.Duration(on[0].DetectedAt-int64(lead)) * time.Second / rate
		if delay < 0 || delay > 300*time.Millisecond {
			t.Errorf("noise %v / speech %v: detected %v after speech start, want ≤ 300ms", c.noiseDB, c.speechDB, delay)
		}
		if start := time.Duration(on[0].StartedAt-int64(lead)) * time.Second / rate; start < -60*time.Millisecond || start > 200*time.Millisecond {
			t.Errorf("onset start estimate off by %v", start)
		}
		if last := ts[len(ts)-1]; last.Kind != Offset {
			t.Errorf("noise %v: no offset after speech ended (last %+v)", c.noiseDB, last)
		}
		if len(on) > 2 {
			t.Errorf("noise %v: speech fragmented into %d onsets", c.noiseDB, len(on))
		}
	}
}

func TestSpeechBuriedInNoiseIsIgnored(t *testing.T) {
	sp := speech(t)
	d := newDetector()
	lead := seconds(2)
	pcm := mix(noise(lead+len(sp), -30, 3), append(make([]int16, lead), scaleTo(sp, -28)...))
	if got := onsets(run(d, pcm)); len(got) != 0 {
		t.Errorf("speech at 2 dB SNR triggered %d onsets", len(got))
	}
}

func TestClickDoesNotTrigger(t *testing.T) {
	d := newDetector()
	pcm := noise(seconds(3), -55, 4)
	click := scaleTo(noise(320, 0, 5), -6) // one loud 20ms frame
	copy(pcm[seconds(2):], click)
	if got := onsets(run(d, pcm)); len(got) != 0 {
		t.Errorf("a single click triggered %d onsets", len(got))
	}
}

func TestPlaybackRaisesTheBar(t *testing.T) {
	sp := speech(t)
	// Soft speech, 18 dB over the floor: enough normally, but no louder
	// than residual echo might be, so not while the assistant talks.
	build := func() []int16 {
		lead := seconds(2)
		return mix(noise(lead+len(sp), -50, 6), append(make([]int16, lead), scaleTo(sp, -32)...))
	}
	normal := newDetector()
	if len(onsets(run(normal, build()))) == 0 {
		t.Fatal("expected onset without playback")
	}
	playing := newDetector()
	playing.SetPlayback(true)
	if got := onsets(run(playing, build())); len(got) != 0 {
		t.Errorf("echo-level speech triggered %d onsets during playback", len(got))
	}
	loud := newDetector()
	loud.SetPlayback(true)
	lead := seconds(2)
	pcm := mix(noise(lead+len(sp), -50, 7), append(make([]int16, lead), scaleTo(sp, -22)...))
	if len(onsets(run(loud, pcm))) == 0 {
		t.Error("clear speech must still trigger during playback")
	}
}

func TestDigitalSilenceThenSpeech(t *testing.T) {
	sp := speech(t)
	d := newDetector()
	lead := seconds(2)
	pcm := append(make([]int16, lead), scaleTo(sp, -26)...)
	// The fixture has a 380ms pause at its comma: one or two phrases.
	if on := onsets(run(d, pcm)); len(on) < 1 || len(on) > 2 {
		t.Fatalf("onsets = %d, want 1-2", len(on))
	}
}

func TestNoiseStepRecovers(t *testing.T) {
	// A fan switches on: the level jumps 25 dB and stays. That may look
	// like speech at first, but the detector must settle and stop
	// reporting speech instead of sticking forever.
	d := newDetector()
	pcm := append(noise(seconds(3), -65, 8), noise(seconds(25), -40, 9)...)
	ts := run(d, pcm)
	if len(ts) > 0 && ts[len(ts)-1].Kind != Offset {
		t.Fatalf("detector stuck in speech after noise step: %+v", ts)
	}
	if d.Speaking() {
		t.Error("Speaking() true at the end")
	}
	// And afterwards it still hears real speech over the new floor.
	sp := speech(t)
	if len(onsets(run(d, mix(noise(len(sp), -40, 10), scaleTo(sp, -18))))) == 0 {
		t.Error("no onset for speech over the new floor")
	}
}

func TestOddChunkSizes(t *testing.T) {
	sp := speech(t)
	d := newDetector()
	pcm := append(make([]int16, seconds(1)), scaleTo(sp, -26)...)
	var ts []Transition
	for off := 0; off < len(pcm); off += 137 {
		ts = append(ts, d.Process(pcm[off:min(off+137, len(pcm))])...)
	}
	if n := len(onsets(ts)); n < 1 || n > 2 {
		t.Errorf("onsets = %d with 137-sample chunks, want 1-2", n)
	}
}

func TestDigitalSilenceThenRoomNoiseSettlesFast(t *testing.T) {
	// Capture often opens on exact zeros before the room noise arrives.
	d := newDetector()
	pcm := append(make([]int16, seconds(0.2)), noise(seconds(8), -50, 11)...)
	ts := run(d, pcm)
	if len(ts) == 0 {
		return // never mistook the noise for speech: fine
	}
	last := ts[len(ts)-1]
	if last.Kind != Offset {
		t.Fatalf("still 'speaking' on plain room noise: %+v", ts)
	}
	if at := time.Duration(last.DetectedAt) * time.Second / rate; at > 4*time.Second {
		t.Errorf("took %v to recognise room noise as silence, want ≤ 4s", at)
	}
}

func TestSustainedNeedsRecentVoice(t *testing.T) {
	// The fixture's second phrase (0.72-1.40s) has no long pause; room
	// noise runs under everything, as it does in a real room.
	phrase := scaleTo(speech(t)[seconds(0.72):seconds(1.40)], -24)
	d := newDetector()
	run(d, noise(seconds(1), -55, 12))
	run(d, mix(phrase, noise(len(phrase), -55, 17)))
	if got := d.Sustained(); got < 300*time.Millisecond {
		t.Errorf("Sustained after 0.68s of speech = %v", got)
	}
	run(d, noise(seconds(0.15), -55, 13)) // quiet, but still inside hangover
	if !d.Speaking() {
		t.Fatal("expected hangover to keep Speaking true")
	}
	if got := d.Sustained(); got != 0 {
		t.Errorf("Sustained during hangover silence = %v, want 0", got)
	}
}

func TestShortBurstIsNotSustained(t *testing.T) {
	d := newDetector()
	run(d, noise(seconds(1), -55, 14))
	run(d, scaleTo(noise(seconds(0.2), 0, 15), -20)) // a cough
	if got := d.Sustained(); got >= 300*time.Millisecond {
		t.Errorf("a 200ms burst counts as %v sustained", got)
	}
}

func TestTinyConfigDoesNotPanic(t *testing.T) {
	d := New(Config{SampleRate: rate, OnsetWindow: time.Millisecond, Hangover: time.Millisecond})
	sp := scaleTo(speech(t), -24)
	ts := run(d, append(noise(seconds(1), -55, 16), sp...))
	if n := len(onsets(ts)); n == 0 || n > 4 {
		t.Errorf("onsets = %d with minimal windows", n)
	}
}
