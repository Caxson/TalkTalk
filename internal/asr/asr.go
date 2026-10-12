// Package asr transcribes streaming speech.
package asr

import "context"

// Result is one recognition update. Interim results (Final == false) for a
// sentence are superseded by later ones with the same SentenceID; a Final
// result closes the sentence.
type Result struct {
	SentenceID int
	Text       string
	Final      bool
	// Audio offsets of the sentence from the start of the stream, in ms.
	// EndMs is only known for Final results.
	BeginMs, EndMs int
}

// Stream is one continuous recognition session over a live audio feed.
type Stream interface {
	// Write feeds mono 16-bit PCM at the recognizer's sample rate. Audio
	// must keep flowing, silence included, for the stream to stay open.
	Write(ctx context.Context, pcm []int16) error
	// Results delivers updates; it is closed when the stream ends.
	Results() <-chan Result
	// Err reports why the stream ended, once Results is closed. It is nil
	// after a clean Close.
	Err() error
	// Close flushes pending audio and ends the stream.
	Close() error
}

// Recognizer opens streams.
type Recognizer interface {
	SampleRate() int
	Open(ctx context.Context) (Stream, error)
}
