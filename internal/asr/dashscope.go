package asr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Caxson/TalkTalk/internal/audio"
	"github.com/Caxson/TalkTalk/internal/dashscope"
)

const (
	sampleRate   = 16000
	sendFrame    = 100 * time.Millisecond // provider-recommended chunk
	closeTimeout = 3 * time.Second
	resultBuffer = 64
)

// DashScopeConfig configures real-time recognition (Paraformer / Fun-ASR).
type DashScopeConfig struct {
	Conn  dashscope.Config
	Model string // e.g. paraformer-realtime-v2, fun-asr-realtime
	// MaxSentenceSilenceMs is the VAD silence that ends a sentence
	// (provider range 200-6000; 0 means provider default).
	MaxSentenceSilenceMs int
	LanguageHints        []string
}

// DashScope is a Recognizer backed by DashScope real-time ASR.
type DashScope struct{ cfg DashScopeConfig }

var _ Recognizer = (*DashScope)(nil)

// NewDashScope validates cfg.
func NewDashScope(cfg DashScopeConfig) (*DashScope, error) {
	switch {
	case cfg.Conn.APIKey == "":
		return nil, errors.New("asr: APIKey is required")
	case cfg.Model == "":
		return nil, errors.New("asr: Model is required")
	case cfg.MaxSentenceSilenceMs != 0 && (cfg.MaxSentenceSilenceMs < 200 || cfg.MaxSentenceSilenceMs > 6000):
		return nil, fmt.Errorf("asr: MaxSentenceSilenceMs %d outside 200-6000", cfg.MaxSentenceSilenceMs)
	}
	return &DashScope{cfg: cfg}, nil
}

// SampleRate implements Recognizer.
func (d *DashScope) SampleRate() int { return sampleRate }

// Open implements Recognizer. Each stream owns its connection.
func (d *DashScope) Open(ctx context.Context) (Stream, error) {
	conn, err := dashscope.Dial(ctx, d.cfg.Conn)
	if err != nil {
		return nil, fmt.Errorf("asr: %w", err)
	}
	params := map[string]any{
		"format":      "pcm",
		"sample_rate": sampleRate,
		// VAD sentence breaking reacts faster than semantic breaking,
		// which is what turn-taking needs.
		"semantic_punctuation_enabled": false,
	}
	if d.cfg.MaxSentenceSilenceMs > 0 {
		params["max_sentence_silence"] = d.cfg.MaxSentenceSilenceMs
	}
	if len(d.cfg.LanguageHints) > 0 {
		params["language_hints"] = d.cfg.LanguageHints
	}
	task, err := conn.Start(ctx, dashscope.TaskSpec{
		TaskGroup: "audio", Task: "asr", Function: "recognition",
		Model: d.cfg.Model, Parameters: params,
	})
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("asr: start: %w", err)
	}
	s := &dsStream{
		conn:    conn,
		task:    task,
		framer:  audio.NewFramer(sampleRate, sendFrame),
		results: make(chan Result, resultBuffer),
		ended:   make(chan struct{}),
	}
	go s.pump()
	return s, nil
}

type dsStream struct {
	conn *dashscope.Conn
	task *dashscope.Task

	writeMu sync.Mutex
	framer  *audio.Framer

	results chan Result
	ended   chan struct{}
	err     error // set before ended is closed

	closeOnce sync.Once
	closing   bool // guarded by writeMu
}

func (s *dsStream) Results() <-chan Result { return s.results }

func (s *dsStream) Err() error {
	select {
	case <-s.ended:
		return s.err
	default:
		return nil
	}
}

func (s *dsStream) Write(ctx context.Context, pcm []int16) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.closing {
		return errors.New("asr: stream closed")
	}
	select {
	case <-s.ended:
		if s.err != nil {
			return s.err
		}
		return errors.New("asr: stream ended")
	default:
	}
	for _, f := range s.framer.Push(pcm) {
		if err := s.task.SendAudio(ctx, audio.Int16ToBytes(f)); err != nil {
			return fmt.Errorf("asr: %w", err)
		}
	}
	return nil
}

func (s *dsStream) Close() error {
	var err error
	s.closeOnce.Do(func() { err = s.close() })
	return err
}

func (s *dsStream) close() error {
	defer s.conn.Close()

	s.writeMu.Lock()
	s.closing = true
	ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
	defer cancel()
	var sendErr error
	if tail := s.framer.Drain(); len(tail) > 0 {
		sendErr = s.task.SendAudio(ctx, audio.Int16ToBytes(tail))
	}
	if sendErr == nil {
		sendErr = s.task.Finish(ctx, false)
	}
	s.writeMu.Unlock()

	select {
	case <-s.ended:
	case <-ctx.Done():
		return errors.New("asr: timed out waiting for final results")
	}
	if s.err != nil {
		return s.err
	}
	if sendErr != nil {
		select {
		case <-s.ended:
			return nil // server already finished; nothing was lost
		default:
		}
		return fmt.Errorf("asr: finish: %w", sendErr)
	}
	return nil
}

type sentenceOutput struct {
	Sentence *struct {
		SentenceID  int    `json:"sentence_id"`
		Text        string `json:"text"`
		SentenceEnd bool   `json:"sentence_end"`
		BeginTime   *int   `json:"begin_time"`
		EndTime     *int   `json:"end_time"`
		Heartbeat   bool   `json:"heartbeat"`
	} `json:"sentence"`
}

func (s *dsStream) pump() {
	defer close(s.results)
	for ev := range s.task.Events() {
		switch ev.Kind {
		case dashscope.KindResult:
			if r, ok := parseResult(ev.Output); ok {
				s.results <- r
			}
		case dashscope.KindEnd:
			if ev.Err != nil {
				s.err = fmt.Errorf("asr: %w", ev.Err)
			}
		}
	}
	close(s.ended)
}

// parseResult maps a provider sentence, dropping heartbeats and the empty
// sentences emitted for leading silence.
func parseResult(raw json.RawMessage) (Result, bool) {
	var out sentenceOutput
	if json.Unmarshal(raw, &out) != nil || out.Sentence == nil {
		return Result{}, false
	}
	sn := out.Sentence
	if sn.Heartbeat || sn.SentenceID == 0 || strings.TrimSpace(sn.Text) == "" {
		return Result{}, false
	}
	r := Result{SentenceID: sn.SentenceID, Text: sn.Text, Final: sn.SentenceEnd}
	if sn.BeginTime != nil {
		r.BeginMs = *sn.BeginTime
	}
	if sn.EndTime != nil {
		r.EndMs = *sn.EndTime
	}
	return r, true
}
