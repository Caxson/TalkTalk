// Package dialog runs one spoken conversation: it turns recognized speech
// into replies, decides when the user has finished, and handles barge-in.
//
// All conversation state lives in a single event loop (Run). Reply
// generation runs in a per-turn goroutine that reports back through a
// channel; audio it produces reaches the player only while its turn is
// still current, so a cancelled reply can never leak sound.
package dialog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Caxson/TalkTalk/internal/asr"
	"github.com/Caxson/TalkTalk/internal/llm"
	"github.com/Caxson/TalkTalk/internal/tts"
	"github.com/Caxson/TalkTalk/internal/vad"
)

// Player is the playout queue (implemented by playout.Player).
type Player interface {
	Enqueue(pcm []int16)
	Mark(id int)
	Flush() []int
	Played() <-chan int
	SetGain(g float64)
	// Motion timeline (see playout.Player).
	Pending() time.Duration
	EnqueuePos() int64
	AttachMotion(pos int64, payload []byte)
}

// Config tunes conversational behaviour.
type Config struct {
	SystemPrompt string
	// MaxHistory caps retained messages (user + assistant), excluding
	// the system prompt.
	MaxHistory int
	// InterruptMinRunes is how many substantive characters the user must
	// say over the assistant before it stops talking.
	InterruptMinRunes int
	// TurnTimeout bounds one reply from first token to last audio.
	TurnTimeout time.Duration
	// ASRReopenDelay is the pause before reopening a failed ASR stream.
	ASRReopenDelay time.Duration
	// PartialEndpointTimeout is a safety net for the recognizer's own
	// endpointing: if an interim transcript stops changing for this long
	// and no final arrives, the user is taken to be done.
	PartialEndpointTimeout time.Duration

	// Soft barge-in: on detected voice the assistant drops to DuckGain at
	// once, and only stops when words confirm it. DuckHold is how long it
	// stays ducked without confirmation once the user is quiet again.
	DuckGain float64
	DuckHold time.Duration
	VAD      vad.Config
}

func (c Config) withDefaults() Config {
	if c.MaxHistory <= 0 {
		c.MaxHistory = 20
	}
	if c.InterruptMinRunes <= 0 {
		c.InterruptMinRunes = 2
	}
	if c.TurnTimeout <= 0 {
		c.TurnTimeout = 60 * time.Second
	}
	if c.ASRReopenDelay <= 0 {
		c.ASRReopenDelay = 500 * time.Millisecond
	}
	if c.PartialEndpointTimeout <= 0 {
		c.PartialEndpointTimeout = 1200 * time.Millisecond
	}
	if c.DuckGain <= 0 || c.DuckGain >= 1 {
		c.DuckGain = 0.25
	}
	if c.DuckHold <= 0 {
		c.DuckHold = 1500 * time.Millisecond
	}
	return c
}

// Deps are the engine's collaborators.
type Deps struct {
	Recognizer asr.Recognizer
	LLM        llm.Streamer
	TTS        tts.Synthesizer
	Player     Player
	// Lips, if set, starts a lip-sync stream for speech at the given
	// sample rate; its frames drive the avatar in lockstep with audio.
	Lips   func(sampleRate int) (LipStream, error)
	Notify func(Event)
	Logger *slog.Logger
	Now    func() time.Time
}

// Engine runs one conversation. Create with New, feed audio with PushAudio,
// and drive with Run.
type Engine struct {
	cfg Config
	d   Deps

	stream atomic.Pointer[streamRef] // current ASR stream, nil while reopening

	// audioMu makes "is this turn still current?" and the player call one
	// atomic step, so Flush can never interleave with a stale Enqueue.
	audioMu     sync.Mutex
	currentTurn int
	marks       map[int]markInfo
	nextMark    int

	turnEvents chan turnEvent
	voice      chan vad.Transition // from PushAudio's detector
	det        *vad.Detector
	gen        sync.WaitGroup // reply goroutines
	stopped    chan struct{}  // closed when Run returns

	// Loop-owned state.
	state        State
	history      []llm.Message
	pendingUser  []string // user sentences not yet answered
	turn         *turnState
	turnSeq      int
	partial      *asr.Result // latest interim result while nobody is talking
	partialTimer *time.Timer
	yield        yieldState
	// interim marks the pendingUser entry that holds interim (not final)
	// words, so the recognizer's late final replaces it instead of
	// repeating it.
	interim interimSlot
}

type interimSlot struct {
	sentence int // 0: none
	index    int
}

// settleInterim replaces the interim entry for sentence with its final
// wording. It reports whether there was one.
func (e *Engine) settleInterim(sentence int, final string) bool {
	if e.interim.sentence == 0 || e.interim.sentence != sentence || e.interim.index >= len(e.pendingUser) {
		return false
	}
	e.pendingUser[e.interim.index] = final
	e.interim = interimSlot{}
	return true
}

// dropInterim removes the interim entry altogether.
func (e *Engine) dropInterim() {
	if i := e.interim.index; e.interim.sentence != 0 && i < len(e.pendingUser) {
		e.pendingUser = append(e.pendingUser[:i:i], e.pendingUser[i+1:]...)
	}
	e.interim = interimSlot{}
}

type streamRef struct {
	s       asr.Stream
	started time.Time // wall time of the first audio sample sent
	once    sync.Once
}

// markInfo identifies a playout mark: the start of a spoken segment, or
// the end of the whole reply.
type markInfo struct {
	turn int
	text string // segment about to be heard; empty for the end mark
	end  bool
}

type turnState struct {
	id     int
	cancel context.CancelFunc
	// fromSentence is set when the turn was started from a stalled interim
	// result rather than a final one; fromText is its normalized text.
	fromSentence int
	fromText     string
	userText     string
	played       []string
	finalAt      time.Time
	speechEnd    time.Time
	metrics      TurnMetrics
	audible      bool
}

type turnEventKind int

const (
	evLLMFirst turnEventKind = iota + 1
	evTTSFirst
	evAudioOut
	evDone
)

type turnEvent struct {
	turn int
	kind turnEventKind
	at   time.Time
	text string
	err  error
}

// New builds an Engine.
func New(cfg Config, d Deps) (*Engine, error) {
	if d.Recognizer == nil || d.LLM == nil || d.TTS == nil || d.Player == nil {
		return nil, errors.New("dialog: Recognizer, LLM, TTS and Player are required")
	}
	if d.Notify == nil {
		d.Notify = func(Event) {}
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	cfg = cfg.withDefaults()
	vcfg := cfg.VAD
	vcfg.SampleRate = d.Recognizer.SampleRate()
	return &Engine{
		cfg:        cfg,
		d:          d,
		det:        vad.New(vcfg),
		voice:      make(chan vad.Transition, 16),
		marks:      map[int]markInfo{},
		turnEvents: make(chan turnEvent, 64),
		stopped:    make(chan struct{}),
		state:      StateListening,
	}, nil
}

// PushAudio feeds microphone PCM at the recognizer's sample rate. It is
// safe to call from any goroutine; audio is dropped while ASR reconnects.
func (e *Engine) PushAudio(ctx context.Context, pcm []int16) {
	ref := e.stream.Load()
	if ref == nil {
		return
	}
	ref.once.Do(func() { ref.started = e.d.Now() })
	e.detectVoice(pcm)
	if err := ref.s.Write(ctx, pcm); err != nil {
		e.d.Logger.Debug("asr write failed", "err", err)
	}
}

// History returns a copy of the committed conversation. Call it only after
// Run has returned.
func (e *Engine) History() []llm.Message {
	return append([]llm.Message(nil), e.history...)
}

// Run drives the conversation until ctx is done.
func (e *Engine) Run(ctx context.Context) error {
	defer e.waitGenerators()
	defer close(e.stopped)
	defer e.abortTurn()
	e.setState(StateListening)

	for {
		ref, err := e.openASR(ctx)
		if err != nil {
			return err
		}
		results := ref.s.Results()
	loop:
		for {
			select {
			case <-ctx.Done():
				e.stream.Store(nil)
				ref.s.Close()
				return nil
			case r, ok := <-results:
				if !ok {
					e.stream.Store(nil)
					e.d.Logger.Warn("asr stream ended, reopening", "err", ref.s.Err())
					// Nothing can confirm or release a yield while deaf.
					e.resume("asr reconnect")
					ref.s.Close() // release the dead connection before dialing anew
					break loop
				}
				e.onASR(ref, r)
			case id := <-e.d.Player.Played():
				e.onPlayed(id)
			case ev := <-e.turnEvents:
				e.onTurnEvent(ev)
			case <-e.partialFired():
				e.onPartialStalled()
			case tr := <-e.voice:
				e.onVoice(tr)
			case <-e.yieldFired():
				e.onYieldTimer()
			}
		}
	}
}

// waitGenerators bounds shutdown: every reply goroutine watches its turn
// context, which abortTurn has cancelled.
func (e *Engine) waitGenerators() {
	done := make(chan struct{})
	go func() { e.gen.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		e.d.Logger.Error("reply goroutines did not stop after shutdown")
	}
}

func (e *Engine) openASR(ctx context.Context) (*streamRef, error) {
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(e.cfg.ASRReopenDelay):
			}
		}
		s, err := e.d.Recognizer.Open(ctx)
		if err == nil {
			ref := &streamRef{s: s}
			e.stream.Store(ref)
			return ref, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		e.d.Logger.Warn("asr open failed", "attempt", attempt+1, "err", err)
		e.notify(Event{Type: "error", Error: "speech recognition unavailable, retrying"})
	}
}

// ---- ASR ----

func (e *Engine) onASR(ref *streamRef, r asr.Result) {
	e.notify(Event{Type: "user", Text: r.Text, Final: r.Final})
	words := substantive(r.Text)
	e.d.Logger.Debug("asr result", "text", r.Text, "final", r.Final, "substantive", words, "state", e.state)

	if r.Final && e.partial != nil && e.partial.SentenceID == r.SentenceID {
		e.clearPartial()
	}
	if e.reconcileEarlyTurn(r) {
		return
	}

	switch e.state {
	case StateSpeaking:
		if words >= e.interruptThreshold() {
			e.bargeIn()
		} else if e.yield.active && r.Final {
			// Words arrived and they are only a backchannel.
			e.resume("backchannel")
		}
	case StateThinking:
		// The user kept talking before we made a sound: retract the
		// speculative reply and answer everything together later.
		if words > 0 {
			e.retractTurn()
		}
	}

	if !r.Final {
		if words > 0 && e.turn == nil {
			e.trackPartial(r)
		}
		return
	}
	if words == 0 {
		if e.turn != nil {
			e.notify(Event{Type: "backchannel", Turn: e.turn.id, Text: r.Text})
		}
		return
	}
	if e.turn != nil {
		// Still talking: the utterance was too short to interrupt, so it
		// is a backchannel ("好", "对") rather than a new request.
		e.notify(Event{Type: "backchannel", Turn: e.turn.id, Text: r.Text})
		return
	}
	if !e.settleInterim(r.SentenceID, r.Text) {
		e.pendingUser = append(e.pendingUser, r.Text)
	}
	var speechEnd time.Time
	if !ref.started.IsZero() && r.EndMs > 0 {
		speechEnd = ref.started.Add(time.Duration(r.EndMs) * time.Millisecond)
	}
	e.startTurn(speechEnd)
}

// reconcileEarlyTurn handles recognizer output for the very sentence an
// early (partial-started) turn is answering. It returns true when the
// result has been fully dealt with.
func (e *Engine) reconcileEarlyTurn(r asr.Result) bool {
	t := e.turn
	if t == nil || t.fromSentence == 0 || t.fromSentence != r.SentenceID {
		return false
	}
	norm := normalizeText(r.Text)
	switch {
	case norm == t.fromText:
		// A repeated interim, or the late final confirming what we
		// answered: adopt the final wording (punctuation) for history.
		if r.Final && e.settleInterim(r.SentenceID, r.Text) {
			t.userText = joinUtterances(e.pendingUser)
			t.fromSentence = 0
		}
		return true
	case !r.Final && strings.HasPrefix(t.fromText, norm):
		return true // an older, shorter interim arriving late
	}
	// Different or additional words for the same sentence.
	if t.audible {
		if added := addedRunes(t.fromText, norm); added >= e.cfg.InterruptMinRunes {
			// The user is still talking over the reply: stop and let the
			// rest of the sentence become the next turn.
			e.bargeIn()
			return false
		}
		// A minor correction: too late to take back, keep history true.
		if r.Final && e.settleInterim(r.SentenceID, r.Text) {
			t.userText = joinUtterances(e.pendingUser)
			t.fromSentence = 0
		}
		return true
	}
	e.retractTurn()
	// Drop the interim words; the final (or a later one) replaces them.
	e.dropInterim()
	return false
}

func (e *Engine) trackPartial(r asr.Result) {
	if e.partial != nil && e.partial.SentenceID == r.SentenceID && e.partial.Text == r.Text {
		return // a repeat does not mean the user is still talking
	}
	p := r
	e.partial = &p
	if e.partialTimer == nil {
		e.partialTimer = time.NewTimer(e.cfg.PartialEndpointTimeout)
		return
	}
	e.partialTimer.Reset(e.cfg.PartialEndpointTimeout)
}

func (e *Engine) clearPartial() {
	e.partial = nil
	if e.partialTimer != nil {
		e.partialTimer.Stop()
	}
}

// partialFired is the timer channel, or nil (blocks forever) when unset.
func (e *Engine) partialFired() <-chan time.Time {
	if e.partialTimer == nil || e.partial == nil {
		return nil
	}
	return e.partialTimer.C
}

// onPartialStalled answers an interim transcript the recognizer never
// closed, so a missed endpoint costs a second instead of a stuck call.
func (e *Engine) onPartialStalled() {
	p := e.partial
	e.partial = nil
	if p == nil || e.turn != nil {
		return
	}
	e.d.Logger.Info("recognizer endpoint missed, answering interim transcript",
		"sentence", p.SentenceID, "text", p.Text, "after", e.cfg.PartialEndpointTimeout)
	e.pendingUser = append(e.pendingUser, p.Text)
	e.interim = interimSlot{sentence: p.SentenceID, index: len(e.pendingUser) - 1}
	e.startTurn(time.Time{})
	e.turn.fromSentence = p.SentenceID
	e.turn.fromText = normalizeText(p.Text)
}

// ---- turns ----

func (e *Engine) startTurn(speechEnd time.Time) {
	e.turnSeq++
	id := e.turnSeq
	userText := joinUtterances(e.pendingUser)
	msgs := e.promptFor(userText)

	ctx, cancel := context.WithTimeout(context.Background(), e.cfg.TurnTimeout)
	now := e.d.Now()
	e.turn = &turnState{id: id, cancel: cancel, userText: userText, finalAt: now, speechEnd: speechEnd}
	e.turn.metrics.Turn = id
	if !speechEnd.IsZero() && now.After(speechEnd) {
		e.turn.metrics.SpeechEndToFinal = now.Sub(speechEnd)
	}

	e.audioMu.Lock()
	e.currentTurn = id
	e.audioMu.Unlock()

	e.notify(Event{Type: "turn", Turn: id, Text: userText})
	e.setState(StateThinking)
	e.d.Logger.Info("turn started", "turn", id, "user", userText)
	e.gen.Add(1)
	go e.generate(ctx, id, msgs)
}

func (e *Engine) promptFor(userText string) []llm.Message {
	msgs := make([]llm.Message, 0, len(e.history)+2)
	if e.cfg.SystemPrompt != "" {
		msgs = append(msgs, llm.Message{Role: llm.RoleSystem, Content: e.cfg.SystemPrompt})
	}
	msgs = append(msgs, e.history...)
	return append(msgs, llm.Message{Role: llm.RoleUser, Content: userText})
}

// detachTurn makes the current turn's audio unreachable and returns marks
// for segments that will now never play.
func (e *Engine) detachTurn(flush bool) {
	e.audioMu.Lock()
	e.currentTurn = 0
	if flush {
		e.d.Player.Flush()
	}
	for id, m := range e.marks {
		if m.turn == e.turn.id {
			delete(e.marks, id)
		}
	}
	e.audioMu.Unlock()
	e.turn.cancel()
}

// retractTurn abandons a reply the user never heard.
func (e *Engine) retractTurn() {
	if e.turn == nil {
		return
	}
	e.d.Logger.Info("turn retracted, user still speaking", "turn", e.turn.id)
	e.detachTurn(true)
	e.notify(Event{Type: "retracted", Turn: e.turn.id})
	e.turn = nil
	e.setState(StateListening)
}

// bargeIn stops the assistant mid-reply. What the user actually heard is
// kept in history so the model knows where it was cut off.
func (e *Engine) bargeIn() {
	if e.turn == nil {
		return
	}
	t := e.turn
	e.d.Logger.Info("barge-in", "turn", t.id, "played_segments", len(t.played))
	e.detachTurn(true)
	heard := strings.Join(t.played, "")
	if heard == "" {
		heard = "（刚开口就被打断了）"
	} else {
		heard += "……（被用户打断）"
	}
	e.commit(t.userText, heard)
	e.turn = nil
	e.notify(Event{Type: "interrupted", Turn: t.id})
	e.setState(StateListening)
}

// abortTurn cancels any turn without touching history (shutdown).
func (e *Engine) abortTurn() {
	if e.turn != nil {
		e.detachTurn(true)
		e.turn = nil
	}
}

func (e *Engine) commit(user, assistant string) {
	e.pendingUser = nil
	e.interim = interimSlot{}
	e.history = append(e.history,
		llm.Message{Role: llm.RoleUser, Content: user},
		llm.Message{Role: llm.RoleAssistant, Content: assistant})
	if over := len(e.history) - e.cfg.MaxHistory; over > 0 {
		// Drop whole user/assistant pairs from the front.
		over += over % 2
		e.history = append([]llm.Message(nil), e.history[over:]...)
	}
}

func (e *Engine) onTurnEvent(ev turnEvent) {
	t := e.turn
	if t == nil || ev.turn != t.id {
		return // a retracted or interrupted turn finishing up
	}
	since := ev.at.Sub(t.finalAt)
	switch ev.kind {
	case evLLMFirst:
		t.metrics.LLMFirstToken = since
	case evTTSFirst:
		t.metrics.TTSFirstAudio = since
	case evAudioOut:
		t.metrics.FirstAudioOut = since
		if !t.speechEnd.IsZero() {
			t.metrics.Total = ev.at.Sub(t.speechEnd)
		}
		t.audible = true
		e.setState(StateSpeaking)
		m := t.metrics
		e.notify(Event{Type: "metrics", Turn: t.id, Metrics: &m})
		e.d.Logger.Info("turn latency", "turn", t.id,
			"speech_end_to_final", m.SpeechEndToFinal, "llm_first_token", m.LLMFirstToken,
			"tts_first_audio", m.TTSFirstAudio, "first_audio_out", m.FirstAudioOut, "total", m.Total)
	case evDone:
		if ev.err != nil && !errors.Is(ev.err, context.Canceled) {
			e.d.Logger.Error("turn failed", "turn", t.id, "err", ev.err)
			e.notify(Event{Type: "error", Turn: t.id, Error: "reply failed"})
			if !t.audible {
				// Nothing was said; keep the user's words for the next try.
				e.detachTurn(true)
				e.notify(Event{Type: "retracted", Turn: t.id})
				e.turn = nil
				e.setState(StateListening)
			}
		}
	}
}

func (e *Engine) onPlayed(id int) {
	e.audioMu.Lock()
	m, ok := e.marks[id]
	delete(e.marks, id)
	e.audioMu.Unlock()
	t := e.turn
	if !ok || t == nil || m.turn != t.id {
		return
	}
	if !m.end {
		// The segment just started playing: from here on the user has
		// (at least partly) heard it.
		t.played = append(t.played, m.text)
		e.notify(Event{Type: "assistant", Turn: t.id, Text: m.text})
		return
	}
	e.commit(t.userText, strings.Join(t.played, ""))
	t.cancel()
	e.audioMu.Lock()
	e.currentTurn = 0
	e.audioMu.Unlock()
	e.turn = nil
	if e.yield.active {
		e.d.Player.SetGain(1) // no Flush on a natural end
	}
	e.d.Logger.Info("turn complete", "turn", t.id)
	e.setState(StateListening)
}

// ---- generation (per-turn goroutine) ----

func (e *Engine) generate(ctx context.Context, id int, msgs []llm.Message) {
	defer e.gen.Done()
	report := func(kind turnEventKind, text string, err error) {
		select {
		case e.turnEvents <- turnEvent{turn: id, kind: kind, at: e.d.Now(), text: text, err: err}:
		case <-ctx.Done():
		}
	}

	segments := make(chan string, 32)
	llmDone := make(chan error, 1)
	e.gen.Add(1)
	go func() {
		defer e.gen.Done()
		llmDone <- e.segmentReply(ctx, msgs, segments, func() { report(evLLMFirst, "", nil) })
	}()

	spoke, err := e.speak(ctx, id, segments, report)
	if err == nil {
		err = <-llmDone // segments closed: the LLM side has finished
	} else if errors.Is(err, errDetached) {
		return // the turn is no longer current; nobody listens
	}
	if err == nil && !spoke {
		// The model said nothing speakable; close the turn anyway.
		report(evAudioOut, "", nil)
	}
	e.finish(id, err)
}

// errDetached means the turn was retracted or interrupted mid-flight.
var errDetached = errors.New("dialog: turn detached")

// segmentReply streams the reply into speakable segments. It always closes
// segments and returns why it stopped (nil, an LLM error, or ctx's error).
func (e *Engine) segmentReply(ctx context.Context, msgs []llm.Message, segments chan<- string, onFirst func()) error {
	defer close(segments)
	send := func(seg string) error {
		select {
		case segments <- seg:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	seg := NewSegmenter()
	first := true
	for delta, err := range e.d.LLM.Stream(ctx, msgs) {
		if err != nil {
			return fmt.Errorf("llm: %w", err)
		}
		if ctx.Err() != nil {
			// Providers may still hand over buffered text after a
			// cancel; none of it should be spoken.
			return ctx.Err()
		}
		if first {
			first = false
			onFirst()
		}
		for _, s := range seg.Push(delta) {
			if err := send(s); err != nil {
				return err
			}
		}
	}
	if s, ok := seg.Flush(); ok {
		return send(s)
	}
	return ctx.Err()
}

// speak synthesizes segments in order into the player and reports whether
// any audio was queued. The error is nil once segments is drained,
// errDetached if the turn stopped being current, or whatever stopped
// synthesis (including ctx's).
func (e *Engine) speak(ctx context.Context, id int, segments <-chan string, report func(turnEventKind, string, error)) (bool, error) {
	firstAudio, ttsReported := true, false
	for text := range segments {
		v := e.newVoice(id, text)
		for pcm, err := range e.d.TTS.Synthesize(ctx, text) {
			if err != nil {
				return !firstAudio, fmt.Errorf("tts: %w", err)
			}
			if !ttsReported {
				ttsReported = true
				report(evTTSFirst, "", nil)
			}
			queued, err := v.push(pcm)
			if err != nil {
				return !firstAudio, err
			}
			if queued && firstAudio {
				firstAudio = false
				report(evAudioOut, "", nil)
			}
		}
		// Synthesizers may stop silently on cancellation.
		if err := ctx.Err(); err != nil {
			return !firstAudio, err
		}
		queued, err := v.finish()
		if err != nil {
			return !firstAudio, err
		}
		if queued && firstAudio {
			firstAudio = false
			report(evAudioOut, "", nil)
		}
	}
	return !firstAudio, nil
}

// finish closes a turn from the generator side. The end mark lets queued
// audio play out and be committed as what was heard, whatever stopped the
// reply. The done event must never be dropped: a turn that never hears it
// would wait forever.
func (e *Engine) finish(id int, err error) {
	e.mark(id, markInfo{turn: id, end: true})
	select {
	case e.turnEvents <- turnEvent{turn: id, kind: evDone, at: e.d.Now(), err: err}:
	case <-e.stopped:
	}
}

func (e *Engine) enqueue(turn int, pcm []int16) bool {
	e.audioMu.Lock()
	defer e.audioMu.Unlock()
	if e.currentTurn != turn {
		return false
	}
	e.d.Player.Enqueue(pcm)
	return true
}

func (e *Engine) mark(turn int, m markInfo) bool {
	e.audioMu.Lock()
	defer e.audioMu.Unlock()
	if e.currentTurn != turn {
		return false
	}
	e.nextMark++
	e.marks[e.nextMark] = m
	e.d.Player.Mark(e.nextMark)
	return true
}

// ---- notifications ----

func (e *Engine) setState(s State) {
	e.state = s
	e.det.SetPlayback(s == StateSpeaking)
	if s != StateSpeaking {
		e.endYield()
	}
	e.notify(Event{Type: "state", State: s})
}

func (e *Engine) notify(ev Event) {
	ev.Timestamp = e.d.Now()
	e.d.Notify(ev)
}
