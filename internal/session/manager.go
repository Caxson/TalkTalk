// Package session assembles one live conversation per browser connection:
// WebRTC peer, playout, and dialog engine, with a shared lifetime.
package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/pion/webrtc/v4"

	"github.com/Caxson/TalkTalk/internal/asr"
	"github.com/Caxson/TalkTalk/internal/dialog"
	"github.com/Caxson/TalkTalk/internal/lipsync"
	"github.com/Caxson/TalkTalk/internal/llm"
	"github.com/Caxson/TalkTalk/internal/playout"
	"github.com/Caxson/TalkTalk/internal/rtc"
	"github.com/Caxson/TalkTalk/internal/tts"
)

// ErrCapacity is returned when the session limit is reached.
var ErrCapacity = errors.New("session: too many active sessions")

// ErrUnknownCharacter is returned for a character that is not in the cast.
var ErrUnknownCharacter = errors.New("session: unknown character")

// SynthFactory builds a per-session synthesizer speaking with voice (empty:
// the provider default); it may hold a warm provider connection, so it is
// closed with the session.
type SynthFactory func(voice string) (Synth, error)

// Persona is who the assistant is in one session.
type Persona struct {
	Character    string // id, for logs
	Voice        string
	SystemPrompt string
}

// Synth is a synthesizer with a lifecycle.
type Synth interface {
	tts.Synthesizer
	Warm(ctx context.Context) error
	Close() error
}

// Deps are shared across sessions.
type Deps struct {
	Recognizer asr.Recognizer
	LLM        llm.Streamer
	NewSynth   SynthFactory
	Dialog     dialog.Config
	// Persona resolves a requested character ("" = default). It returns
	// ErrUnknownCharacter for ids it does not know. Nil: everyone gets
	// Dialog.SystemPrompt and the default voice.
	Persona func(character string) (Persona, error)
	// Lips enables avatar motion; nil means audio only.
	Lips       func(sampleRate int) (dialog.LipStream, error)
	ICEServers []string
	Max        int
	Logger     *slog.Logger
}

// Manager owns all live sessions.
type Manager struct {
	d Deps

	mu       sync.Mutex
	sessions map[string]*Session
	closed   bool
}

// NewManager validates deps.
func NewManager(d Deps) (*Manager, error) {
	if d.Recognizer == nil || d.LLM == nil || d.NewSynth == nil {
		return nil, errors.New("session: Recognizer, LLM and NewSynth are required")
	}
	if d.Max <= 0 {
		d.Max = 1
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Manager{d: d, sessions: map[string]*Session{}}, nil
}

// Session is one live conversation.
type Session struct {
	ID     string
	cancel context.CancelFunc
	done   chan struct{}
}

// Done is closed once the session has fully shut down.
func (s *Session) Done() <-chan struct{} { return s.done }

// Count returns the number of live sessions.
func (m *Manager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sessions)
}

// Create answers the client's offer and starts a conversation with the
// given character ("" = default).
func (m *Manager) Create(ctx context.Context, offer webrtc.SessionDescription, character string) (*Session, webrtc.SessionDescription, error) {
	persona, err := m.persona(character)
	if err != nil {
		return nil, webrtc.SessionDescription{}, err
	}
	id := uuid.NewString()
	if err := m.reserve(id); err != nil {
		return nil, webrtc.SessionDescription{}, err
	}
	s, answer, err := m.start(ctx, id, offer, persona)
	if err != nil {
		m.release(id, nil)
		return nil, webrtc.SessionDescription{}, err
	}
	m.mu.Lock()
	_, slotHeld := m.sessions[id]
	if slotHeld {
		m.sessions[id] = s
	}
	closing := m.closed
	m.mu.Unlock()
	// The session may already have torn itself down (client vanished during
	// negotiation), or CloseAll may have run while it was connecting.
	if !slotHeld || closing {
		s.cancel()
	}
	return s, answer, nil
}

func (m *Manager) persona(character string) (Persona, error) {
	if m.d.Persona == nil {
		if character != "" {
			return Persona{}, fmt.Errorf("%w: %q", ErrUnknownCharacter, character)
		}
		return Persona{SystemPrompt: m.d.Dialog.SystemPrompt}, nil
	}
	return m.d.Persona(character)
}

func (m *Manager) reserve(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errors.New("session: manager closed")
	}
	if len(m.sessions) >= m.d.Max {
		return ErrCapacity
	}
	m.sessions[id] = nil // placeholder holds the slot while connecting
	return nil
}

// release frees id's slot if it still belongs to s (nil: the placeholder).
func (m *Manager) release(id string, s *Session) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cur, ok := m.sessions[id]; ok && (cur == nil || cur == s) {
		delete(m.sessions, id)
	}
}

func (m *Manager) start(ctx context.Context, id string, offer webrtc.SessionDescription, persona Persona) (*Session, webrtc.SessionDescription, error) {
	log := m.d.Logger.With("session", id)
	synth, err := m.d.NewSynth(persona.Voice)
	if err != nil {
		return nil, webrtc.SessionDescription{}, fmt.Errorf("session: synth: %w", err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	s := &Session{ID: id, cancel: cancel, done: make(chan struct{})}

	var peer atomic.Pointer[rtc.Peer]
	player, err := playout.New(synth.SampleRate(), func(pkt []byte, d time.Duration) error {
		if p := peer.Load(); p != nil {
			return p.WriteAudio(pkt, d)
		}
		return nil
	})
	if err != nil {
		cancel()
		synth.Close()
		return nil, webrtc.SessionDescription{}, err
	}
	dcfg := m.d.Dialog
	dcfg.SystemPrompt = persona.SystemPrompt
	engine, err := dialog.New(dcfg, dialog.Deps{
		Recognizer: m.d.Recognizer,
		LLM:        m.d.LLM,
		TTS:        synth,
		Player:     player,
		Lips:       m.d.Lips,
		Notify: func(ev dialog.Event) {
			if p := peer.Load(); p != nil {
				p.Send(ev)
			}
		},
		Logger: log,
	})
	if err != nil {
		cancel()
		synth.Close()
		return nil, webrtc.SessionDescription{}, err
	}

	p, answer, err := rtc.Answer(ctx, offer, rtc.Config{
		ICEServers:      m.d.ICEServers,
		InputSampleRate: m.d.Recognizer.SampleRate(),
		OnAudio:         func(pcm []int16) { engine.PushAudio(runCtx, pcm) },
		OnMessage:       func(msg []byte) { onClientMessage(engine, msg) },
		OnClose:         cancel,
		Logger:          log,
	})
	if err != nil {
		cancel()
		synth.Close()
		return nil, webrtc.SessionDescription{}, err
	}
	peer.Store(p)
	if m.d.Lips != nil {
		player.SetMotionSink(p.SendMotion)
		names := lipsync.Names
		p.Send(dialog.Event{Type: "avatar", Names: names[:], FrameRate: lipsync.FrameRate, Timestamp: time.Now()})
	}

	go func() {
		// Pre-dial TTS so the first reply skips the handshake.
		if err := synth.Warm(runCtx); err != nil {
			log.Warn("tts warm-up failed", "err", err)
		}
	}()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := player.Run(runCtx); err != nil {
			log.Info("playout stopped", "err", err)
		}
		cancel()
	}()
	go func() {
		defer wg.Done()
		if err := engine.Run(runCtx); err != nil {
			log.Error("dialog stopped", "err", err)
		}
		cancel()
	}()
	go func() {
		<-runCtx.Done()
		p.Close()
		wg.Wait()
		synth.Close()
		m.release(id, s)
		log.Info("session closed")
		close(s.done)
	}()
	log.Info("session started", "character", persona.Character, "voice", persona.Voice)
	return s, answer, nil
}

// Close ends one session. It reports false if the id is unknown.
func (m *Manager) Close(id string) bool {
	m.mu.Lock()
	s := m.sessions[id]
	m.mu.Unlock()
	if s == nil {
		return false
	}
	s.cancel()
	return true
}

// CloseAll ends every session, refuses new ones, and waits for shutdown
// up to ctx's deadline.
func (m *Manager) CloseAll(ctx context.Context) {
	m.mu.Lock()
	m.closed = true
	var live []*Session
	for _, s := range m.sessions {
		if s != nil {
			live = append(live, s)
		}
	}
	m.mu.Unlock()
	for _, s := range live {
		s.cancel()
	}
	for _, s := range live {
		select {
		case <-s.done:
		case <-ctx.Done():
			return
		}
	}
}

// Start is Create returning only the session id, for the HTTP layer.
func (m *Manager) Start(ctx context.Context, offer webrtc.SessionDescription, character string) (string, webrtc.SessionDescription, error) {
	s, answer, err := m.Create(ctx, offer, character)
	if err != nil {
		return "", webrtc.SessionDescription{}, err
	}
	return s.ID, answer, nil
}

// clientMessage is what the browser may send on the events channel.
type clientMessage struct {
	Type string `json:"type"`
	// perception: a camera reading, computed in the browser.
	Face  bool `json:"face"`
	Mouth bool `json:"mouth"`
}

func onClientMessage(engine *dialog.Engine, raw []byte) {
	var m clientMessage
	if json.Unmarshal(raw, &m) != nil {
		return
	}
	switch m.Type {
	case "perception":
		engine.SetVisual(dialog.Visual{FacePresent: m.Face, MouthActive: m.Mouth})
	}
}
