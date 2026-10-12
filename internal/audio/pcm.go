// Package audio holds PCM utilities and the Opus codec used on the WebRTC
// leg. All PCM is mono, signed 16-bit, little-endian.
package audio

import (
	"encoding/binary"
	"fmt"
	"math"
	"time"
)

// Int16ToBytes encodes samples as little-endian bytes.
func Int16ToBytes(s []int16) []byte {
	b := make([]byte, 2*len(s))
	for i, v := range s {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(v))
	}
	return b
}

// BytesToInt16 decodes little-endian bytes into samples.
func BytesToInt16(b []byte) ([]int16, error) {
	if len(b)%2 != 0 {
		return nil, fmt.Errorf("audio: odd PCM byte count %d", len(b))
	}
	s := make([]int16, len(b)/2)
	for i := range s {
		s[i] = int16(binary.LittleEndian.Uint16(b[2*i:]))
	}
	return s, nil
}

// RMSdBFS returns the RMS level relative to full scale; -Inf for silence.
func RMSdBFS(s []int16) float64 {
	if len(s) == 0 {
		return math.Inf(-1)
	}
	var sum float64
	for _, v := range s {
		f := float64(v) / 32768
		sum += f * f
	}
	if sum == 0 {
		return math.Inf(-1)
	}
	return 10 * math.Log10(sum/float64(len(s)))
}

// Framer re-chunks a PCM stream of arbitrary chunk sizes into fixed-length
// frames. It is not safe for concurrent use.
type Framer struct {
	size int
	buf  []int16
}

// NewFramer returns a Framer emitting frames of the given duration.
func NewFramer(sampleRate int, frame time.Duration) *Framer {
	return &Framer{size: int(int64(sampleRate) * int64(frame) / int64(time.Second))}
}

// Push appends samples and returns every complete frame. Returned frames are
// fresh slices, safe to retain.
func (f *Framer) Push(s []int16) [][]int16 {
	f.buf = append(f.buf, s...)
	var out [][]int16
	for len(f.buf) >= f.size {
		out = append(out, append([]int16(nil), f.buf[:f.size]...))
		f.buf = f.buf[f.size:]
	}
	if len(f.buf) == 0 {
		f.buf = nil
	}
	return out
}

// Flush returns the remaining samples zero-padded to a full frame.
func (f *Framer) Flush() ([]int16, bool) {
	if len(f.buf) == 0 {
		return nil, false
	}
	out := make([]int16, f.size)
	copy(out, f.buf)
	f.buf = nil
	return out, true
}

// Drain returns the remaining samples unpadded (nil if none).
func (f *Framer) Drain() []int16 {
	out := f.buf
	f.buf = nil
	return out
}

// Reset discards buffered samples.
func (f *Framer) Reset() { f.buf = nil }
