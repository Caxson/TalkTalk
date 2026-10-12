// Package tts synthesizes speech from text.
package tts

import (
	"context"
	"iter"
)

// Synthesizer turns one text segment into mono 16-bit PCM at SampleRate.
//
// The sequence yields PCM chunks as they arrive, then stops; on failure it
// yields (nil, err) once. Cancelling ctx or breaking out of the loop aborts
// synthesis promptly. Implementations serialize concurrent calls.
type Synthesizer interface {
	SampleRate() int
	Synthesize(ctx context.Context, text string) iter.Seq2[[]int16, error]
}
