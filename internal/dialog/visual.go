package dialog

import (
	"strings"
	"time"
)

// Visual is a reading of the user from the browser's camera analysis.
// Video never leaves the browser; only these facts do.
type Visual struct {
	// FacePresent: a face is in view.
	FacePresent bool
	// MouthActive: the mouth moved recently (the client looks back a few
	// hundred milliseconds, which also covers the mouth opening slightly
	// before the sound comes out).
	MouthActive bool
}

const (
	// visualFresh: older readings say nothing about what the user is doing now.
	visualFresh = 500 * time.Millisecond
	// longVoice: voice sustained this long is the user talking, whatever
	// the camera reads (profile, mask, a mouth that barely opens).
	longVoice = time.Second
)

type visualReading struct {
	v  Visual
	at time.Time
}

// SetVisual records the latest camera reading. Safe from any goroutine.
func (e *Engine) SetVisual(v Visual) {
	e.visual.Store(&visualReading{v: v, at: e.d.Now()})
}

// echoLikely reports that the user is visibly not talking: on camera,
// mouth still, reading fresh. Sound the assistant hears then is its own
// voice leaking back from the speakers. Without a camera, or with nobody
// in view, it is always false and nothing changes.
func (e *Engine) echoLikely() bool {
	r := e.visual.Load()
	if r == nil || e.d.Now().Sub(r.at) > visualFresh {
		return false
	}
	return r.v.FacePresent && !r.v.MouthActive
}

// mouthSeenMoving reports a fresh camera reading of a talking face.
func (e *Engine) mouthSeenMoving() bool {
	r := e.visual.Load()
	return r != nil && e.d.Now().Sub(r.at) <= visualFresh && r.v.FacePresent && r.v.MouthActive
}

// isEcho decides whether words heard while the assistant speaks are its
// own voice coming back. Two independent signs: the words match what it
// is saying (no camera needed), or the user is visibly not talking. The
// visual sign only covers short, brief sounds, so a camera that misreads
// the mouth can never lock the user out.
func (e *Engine) isEcho(text string, words int) bool {
	if t := e.turn; t != nil && soundsLikeOwnWords(text, strings.Join(t.played[max(0, len(t.played)-2):], ""), strings.Join(t.played, "")) {
		e.d.Logger.Info("ignoring own words heard back (echo)", "text", text)
		return true
	}
	if e.echoLikely() && words < 2*e.cfg.InterruptMinRunes && e.det.Sustained() < longVoice {
		e.d.Logger.Info("ignoring speech heard while the user's mouth is still (echo)", "text", text)
		return true
	}
	return false
}

// echoTail remembers what the assistant just said: the recognizer often
// delivers the echo of its last sentence after the reply has finished.
type echoTail struct {
	recent, all string
	until       time.Time
}

func (e *Engine) rememberSaid(t *turnState) {
	e.echoTail = echoTail{
		recent: strings.Join(t.played[max(0, len(t.played)-2):], ""),
		all:    strings.Join(t.played, ""),
		until:  e.d.Now().Add(e.cfg.EchoTail),
	}
}

// inEchoTail reports words heard just after a reply that are that reply.
func (e *Engine) inEchoTail(text string) bool {
	tail := e.echoTail
	if e.d.Now().After(tail.until) || !soundsLikeOwnWords(text, tail.recent, tail.all) {
		return false
	}
	e.d.Logger.Info("ignoring late echo of the reply that just ended", "text", text)
	return true
}
