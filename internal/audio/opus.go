package audio

/*
#cgo pkg-config: opus
#include <opus.h>

static int tt_set_bitrate(OpusEncoder *e, opus_int32 v) { return opus_encoder_ctl(e, OPUS_SET_BITRATE(v)); }
*/
import "C"

import (
	"fmt"
	"runtime"
	"time"
	"unsafe"
)

// FrameDuration is the Opus frame length used on the WebRTC leg.
const FrameDuration = 20 * time.Millisecond

const (
	maxOpusPacket = 1500
	// 32 kbps is transparent for wideband speech and cheap on the wire.
	speechBitrate = 32000
)

func validRate(rate int) error {
	switch rate {
	case 8000, 12000, 16000, 24000, 48000:
		return nil
	}
	return fmt.Errorf("audio: opus does not support %d Hz", rate)
}

func opusErr(code C.int) string { return C.GoString(C.opus_strerror(code)) }

// OpusEncoder turns 20ms mono PCM frames into Opus packets.
// It is not safe for concurrent use.
type OpusEncoder struct {
	enc          *C.OpusEncoder
	frameSamples int
	buf          []byte
}

// NewOpusEncoder creates a VoIP-tuned mono encoder for the given input rate.
// The RTP clock stays 48 kHz regardless of the input rate.
func NewOpusEncoder(sampleRate int) (*OpusEncoder, error) {
	if err := validRate(sampleRate); err != nil {
		return nil, err
	}
	var code C.int
	enc := C.opus_encoder_create(C.opus_int32(sampleRate), 1, C.OPUS_APPLICATION_VOIP, &code)
	if code != C.OPUS_OK {
		return nil, fmt.Errorf("audio: opus encoder: %s", opusErr(code))
	}
	if code = C.tt_set_bitrate(enc, speechBitrate); code != C.OPUS_OK {
		C.opus_encoder_destroy(enc)
		return nil, fmt.Errorf("audio: opus bitrate: %s", opusErr(code))
	}
	e := &OpusEncoder{
		enc:          enc,
		frameSamples: sampleRate * int(FrameDuration/time.Millisecond) / 1000,
		buf:          make([]byte, maxOpusPacket),
	}
	runtime.AddCleanup(e, func(p *C.OpusEncoder) { C.opus_encoder_destroy(p) }, enc)
	return e, nil
}

// FrameSamples is the number of samples Encode expects.
func (e *OpusEncoder) FrameSamples() int { return e.frameSamples }

// Encode compresses exactly one frame. The returned packet is a fresh slice.
func (e *OpusEncoder) Encode(frame []int16) ([]byte, error) {
	if len(frame) != e.frameSamples {
		return nil, fmt.Errorf("audio: encode got %d samples, want %d", len(frame), e.frameSamples)
	}
	n := C.opus_encode(e.enc,
		(*C.opus_int16)(unsafe.Pointer(&frame[0])), C.int(len(frame)),
		(*C.uchar)(unsafe.Pointer(&e.buf[0])), C.opus_int32(len(e.buf)))
	runtime.KeepAlive(e)
	if n < 0 {
		return nil, fmt.Errorf("audio: opus encode: %s", opusErr(n))
	}
	return append([]byte(nil), e.buf[:n]...), nil
}

// OpusDecoder turns Opus packets into mono PCM at a chosen output rate;
// libopus resamples internally, so the ASR rate can be requested directly.
// It is not safe for concurrent use.
type OpusDecoder struct {
	dec *C.OpusDecoder
	buf []int16
}

// NewOpusDecoder creates a mono decoder producing PCM at sampleRate.
func NewOpusDecoder(sampleRate int) (*OpusDecoder, error) {
	if err := validRate(sampleRate); err != nil {
		return nil, err
	}
	var code C.int
	dec := C.opus_decoder_create(C.opus_int32(sampleRate), 1, &code)
	if code != C.OPUS_OK {
		return nil, fmt.Errorf("audio: opus decoder: %s", opusErr(code))
	}
	// 120ms is the longest Opus packet.
	d := &OpusDecoder{dec: dec, buf: make([]int16, sampleRate*120/1000)}
	runtime.AddCleanup(d, func(p *C.OpusDecoder) { C.opus_decoder_destroy(p) }, dec)
	return d, nil
}

// Decode returns the PCM in one packet as a fresh slice.
func (d *OpusDecoder) Decode(packet []byte) ([]int16, error) {
	if len(packet) == 0 {
		return nil, fmt.Errorf("audio: empty opus packet")
	}
	n := C.opus_decode(d.dec,
		(*C.uchar)(unsafe.Pointer(&packet[0])), C.opus_int32(len(packet)),
		(*C.opus_int16)(unsafe.Pointer(&d.buf[0])), C.int(len(d.buf)), 0)
	runtime.KeepAlive(d)
	if n < 0 {
		return nil, fmt.Errorf("audio: opus decode: %s", opusErr(n))
	}
	return append([]int16(nil), d.buf[:n]...), nil
}
