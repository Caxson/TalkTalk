package dialog

import (
	"time"

	"github.com/Caxson/TalkTalk/internal/vad"
)

// Soft barge-in. Voice energy is fast but easily fooled (a cough, residual
// echo); recognized words are reliable but slow. So the assistant yields
// on voice, ducking to DuckGain within a few frames, and stops only when
// words confirm the interruption. An unconfirmed yield resumes once the
// user is quiet again.

const (
	// confirmVoice: after yielding this long with the user still audibly
	// talking, a single substantive character confirms the barge-in.
	confirmVoice = 300 * time.Millisecond
	// maxYield bounds how long a yield may last while the user keeps
	// making sound that never turns into words.
	maxYield = 4 * time.Second
)

type yieldState struct {
	active bool
	since  time.Time
	timer  *time.Timer
}

// detectVoice runs on PushAudio's goroutine; transitions go to the loop.
func (e *Engine) detectVoice(pcm []int16) {
	for _, tr := range e.det.Process(pcm) {
		select {
		case e.voice <- tr:
		default: // the loop is far behind; a stale transition is useless
		}
	}
}

func (e *Engine) onVoice(tr vad.Transition) {
	if tr.Kind != vad.Onset || e.state != StateSpeaking || e.yield.active {
		return
	}
	e.yield.active = true
	e.yield.since = e.d.Now()
	e.d.Player.SetGain(e.cfg.DuckGain)
	e.armYield(e.cfg.DuckHold)
	lag := time.Duration(tr.DetectedAt-tr.StartedAt) * time.Second / time.Duration(e.d.Recognizer.SampleRate())
	e.d.Logger.Info("yielding to voice", "turn", e.turnID(), "detect_lag", lag)
	e.notify(Event{Type: "yield", Turn: e.turnID()})
}

// interruptThreshold is the number of substantive characters that stop
// the assistant. With sustained voice behind them, one is enough ("停"):
// two independent signals agree. A cough or a pause does not count.
func (e *Engine) interruptThreshold() int {
	if e.yield.active && e.det.Sustained() >= confirmVoice {
		return 1
	}
	return e.cfg.InterruptMinRunes
}

func (e *Engine) onYieldTimer() {
	if !e.yield.active {
		return
	}
	if e.det.Sustained() > 0 && e.d.Now().Sub(e.yield.since) < maxYield {
		e.armYield(e.cfg.DuckHold / 3) // still talking; words may yet come
		return
	}
	e.resume("no words followed")
}

// resume restores volume after a yield that words did not confirm.
func (e *Engine) resume(reason string) {
	if !e.yield.active {
		return
	}
	e.endYield()
	e.d.Player.SetGain(1)
	e.d.Logger.Info("resuming after yield", "turn", e.turnID(), "reason", reason)
	e.notify(Event{Type: "resume", Turn: e.turnID()})
}

// endYield forgets a yield without touching the player (it was flushed,
// or the reply is over).
func (e *Engine) endYield() {
	e.yield.active = false
	if e.yield.timer != nil {
		e.yield.timer.Stop()
	}
}

func (e *Engine) armYield(d time.Duration) {
	if e.yield.timer == nil {
		e.yield.timer = time.NewTimer(d)
		return
	}
	e.yield.timer.Stop()
	e.yield.timer.Reset(d)
}

// yieldFired is the timer channel, or nil while not yielding.
func (e *Engine) yieldFired() <-chan time.Time {
	if !e.yield.active || e.yield.timer == nil {
		return nil
	}
	return e.yield.timer.C
}

func (e *Engine) turnID() int {
	if e.turn == nil {
		return 0
	}
	return e.turn.id
}
