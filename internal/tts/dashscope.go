package tts

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"sync"
	"time"

	"github.com/Caxson/TalkTalk/internal/audio"
	"github.com/Caxson/TalkTalk/internal/dashscope"
)

// cancelGrace bounds how long a cancelled task may take to acknowledge
// before its connection is abandoned.
const cancelGrace = time.Second

// DashScopeConfig configures CosyVoice over DashScope.
type DashScopeConfig struct {
	Conn  dashscope.Config
	Model string // e.g. cosyvoice-v3-flash
	Voice string // must match the model family, e.g. longxiaochun_v3
	// SampleRate must be valid for both CosyVoice and Opus:
	// 8000, 16000, 24000 or 48000.
	SampleRate int
	Volume     int     // 0-100, 0 means provider default
	Rate       float64 // 0.5-2.0, 0 means provider default
}

// DashScope synthesizes with CosyVoice, keeping one warm connection so only
// the first segment of a session pays the handshake.
type DashScope struct {
	cfg DashScopeConfig

	mu   sync.Mutex // serializes tasks; guards conn
	conn *dashscope.Conn
}

var _ Synthesizer = (*DashScope)(nil)

// NewDashScope validates cfg. No connection is made until Warm or the first
// Synthesize.
func NewDashScope(cfg DashScopeConfig) (*DashScope, error) {
	switch {
	case cfg.Conn.APIKey == "":
		return nil, errors.New("tts: APIKey is required")
	case cfg.Model == "" || cfg.Voice == "":
		return nil, errors.New("tts: Model and Voice are required")
	}
	switch cfg.SampleRate {
	case 8000, 16000, 24000, 48000:
	default:
		return nil, fmt.Errorf("tts: unsupported sample rate %d", cfg.SampleRate)
	}
	return &DashScope{cfg: cfg}, nil
}

// SampleRate implements Synthesizer.
func (d *DashScope) SampleRate() int { return d.cfg.SampleRate }

// Warm dials ahead of the first segment.
func (d *DashScope) Warm(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.connLocked(ctx)
	return err
}

// Close drops the warm connection.
func (d *DashScope) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.conn == nil {
		return nil
	}
	err := d.conn.Close()
	d.conn = nil
	return err
}

func (d *DashScope) connLocked(ctx context.Context) (*dashscope.Conn, error) {
	if d.conn != nil && d.conn.Alive() {
		return d.conn, nil
	}
	c, err := dashscope.Dial(ctx, d.cfg.Conn)
	if err != nil {
		return nil, fmt.Errorf("tts: %w", err)
	}
	d.conn = c
	return c, nil
}

func (d *DashScope) spec() dashscope.TaskSpec {
	p := map[string]any{
		"text_type":   "PlainText",
		"voice":       d.cfg.Voice,
		"format":      "pcm",
		"sample_rate": d.cfg.SampleRate,
	}
	if d.cfg.Volume > 0 {
		p["volume"] = d.cfg.Volume
	}
	if d.cfg.Rate > 0 {
		p["rate"] = d.cfg.Rate
	}
	return dashscope.TaskSpec{TaskGroup: "audio", Task: "tts", Function: "SpeechSynthesizer", Model: d.cfg.Model, Parameters: p}
}

// Synthesize implements Synthesizer.
func (d *DashScope) Synthesize(ctx context.Context, text string) iter.Seq2[[]int16, error] {
	return func(yield func([]int16, error) bool) {
		if strings.TrimSpace(text) == "" {
			return
		}
		d.mu.Lock()
		defer d.mu.Unlock()

		conn, err := d.connLocked(ctx)
		if err != nil {
			yield(nil, err)
			return
		}
		task, err := conn.Start(ctx, d.spec())
		if err != nil {
			yield(nil, fmt.Errorf("tts: start: %w", err))
			return
		}
		if err := task.SendText(ctx, text); err != nil {
			yield(nil, fmt.Errorf("tts: send text: %w", err))
			return
		}
		if err := task.Finish(ctx, false); err != nil {
			yield(nil, fmt.Errorf("tts: finish: %w", err))
			return
		}

		if !d.stream(ctx, task, yield) {
			d.cancel(conn, task)
		}
	}
}

// stream forwards audio until the task ends. It returns false if the task
// is still running because the caller stopped or ctx was cancelled.
func (d *DashScope) stream(ctx context.Context, task *dashscope.Task, yield func([]int16, error) bool) bool {
	var carry []byte // odd trailing byte from the previous frame
	for {
		select {
		case <-ctx.Done():
			return false
		case ev, ok := <-task.Events():
			if !ok {
				return true
			}
			switch ev.Kind {
			case dashscope.KindAudio:
				b := ev.Audio
				if len(carry) > 0 {
					b = append(carry, b...)
					carry = nil
				}
				if len(b)%2 == 1 {
					carry = []byte{b[len(b)-1]}
					b = b[:len(b)-1]
				}
				if len(b) == 0 {
					continue
				}
				pcm, _ := audio.BytesToInt16(b) // even length guaranteed
				if !yield(pcm, nil) {
					return false
				}
			case dashscope.KindEnd:
				if ev.Err != nil {
					yield(nil, fmt.Errorf("tts: %w", ev.Err))
				}
				return true
			}
		}
	}
}

// cancel stops a running task and waits briefly for the server to confirm,
// so the connection can be reused. A task that will not stop takes its
// connection down with it.
func (d *DashScope) cancel(conn *dashscope.Conn, task *dashscope.Task) {
	ctx, done := context.WithTimeout(context.Background(), cancelGrace)
	defer done()
	if err := task.Finish(ctx, true); err != nil {
		conn.Close()
		return
	}
	for {
		select {
		case ev, ok := <-task.Events():
			if !ok || ev.Kind == dashscope.KindEnd {
				return
			}
		case <-ctx.Done():
			conn.Close()
			return
		}
	}
}
