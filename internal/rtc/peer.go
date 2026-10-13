// Package rtc terminates the browser's WebRTC connection: one bidirectional
// Opus audio track plus a data channel for conversation events.
package rtc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"

	"github.com/Caxson/TalkTalk/internal/audio"
)

const (
	gatherTimeout   = 5 * time.Second
	pendingMessages = 64
	// maxClientMessage bounds what a client may send per message.
	maxClientMessage = 1024
	// EventsLabel is the data channel the client opens for events.
	EventsLabel = "events"
	// MotionLabel is the client's unordered, no-retransmit channel for
	// avatar motion frames: a late frame is useless, never worth a stall.
	MotionLabel = "motion"
)

// Config for a peer.
type Config struct {
	ICEServers []string // e.g. stun:stun.example.com:3478; empty for LAN/localhost
	// InputSampleRate is the PCM rate delivered to OnAudio.
	InputSampleRate int
	// OnAudio receives decoded microphone audio. Called from one goroutine.
	OnAudio func(pcm []int16)
	// OnClose fires once when the connection fails or closes.
	OnClose func()
	// OnMessage receives text messages the client sends on the events
	// channel (e.g. camera readings). Optional.
	OnMessage func(msg []byte)
	Logger    *slog.Logger
}

// Peer is one browser connection.
type Peer struct {
	pc    *webrtc.PeerConnection
	track *webrtc.TrackLocalStaticSample
	cfg   Config

	mu      sync.Mutex
	dc      *webrtc.DataChannel
	dcOpen  bool
	pending [][]byte
	motion  *webrtc.DataChannel // set once open

	closeOnce sync.Once
	closed    chan struct{}
}

// Answer creates a Peer from the client's offer and returns the complete
// answer (ICE gathering finished, no trickle needed).
func Answer(ctx context.Context, offer webrtc.SessionDescription, cfg Config) (*Peer, webrtc.SessionDescription, error) {
	if cfg.OnAudio == nil || cfg.InputSampleRate == 0 {
		return nil, webrtc.SessionDescription{}, errors.New("rtc: OnAudio and InputSampleRate are required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if offer.Type != webrtc.SDPTypeOffer {
		return nil, webrtc.SessionDescription{}, fmt.Errorf("rtc: want offer, got %s", offer.Type)
	}

	var ice []webrtc.ICEServer
	if len(cfg.ICEServers) > 0 {
		ice = []webrtc.ICEServer{{URLs: cfg.ICEServers}}
	}
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{ICEServers: ice})
	if err != nil {
		return nil, webrtc.SessionDescription{}, fmt.Errorf("rtc: new peer: %w", err)
	}
	p := &Peer{pc: pc, cfg: cfg, closed: make(chan struct{})}
	fail := func(err error) (*Peer, webrtc.SessionDescription, error) {
		p.Close()
		return nil, webrtc.SessionDescription{}, err
	}

	p.track, err = webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2},
		"audio", "talktalk")
	if err != nil {
		return fail(fmt.Errorf("rtc: local track: %w", err))
	}
	sender, err := pc.AddTrack(p.track)
	if err != nil {
		return fail(fmt.Errorf("rtc: add track: %w", err))
	}
	go drainRTCP(sender)

	pc.OnTrack(p.onTrack)
	pc.OnDataChannel(p.onDataChannel)
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		cfg.Logger.Info("peer connection state", "state", s.String())
		switch s {
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed, webrtc.PeerConnectionStateDisconnected:
			p.Close()
		}
	})

	if err := pc.SetRemoteDescription(offer); err != nil {
		return fail(fmt.Errorf("rtc: set offer: %w", err))
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		return fail(fmt.Errorf("rtc: create answer: %w", err))
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		return fail(fmt.Errorf("rtc: set answer: %w", err))
	}
	ctx, cancel := context.WithTimeout(ctx, gatherTimeout)
	defer cancel()
	select {
	case <-gathered:
	case <-ctx.Done():
		return fail(fmt.Errorf("rtc: ICE gathering: %w", ctx.Err()))
	}
	return p, *pc.LocalDescription(), nil
}

// WriteAudio sends one encoded Opus frame. It matches playout.Sink.
func (p *Peer) WriteAudio(packet []byte, d time.Duration) error {
	select {
	case <-p.closed:
		return errors.New("rtc: peer closed")
	default:
	}
	return p.track.WriteSample(media.Sample{Data: packet, Duration: d})
}

// Send delivers v as JSON on the events channel, queuing until it opens.
func (p *Peer) Send(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		p.cfg.Logger.Error("encode event", "err", err)
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.dcOpen {
		if len(p.pending) < pendingMessages {
			p.pending = append(p.pending, b)
		}
		return
	}
	if err := p.dc.SendText(string(b)); err != nil {
		p.cfg.Logger.Debug("send event", "err", err)
	}
}

// Done is closed when the peer has closed.
func (p *Peer) Done() <-chan struct{} { return p.closed }

// Close tears the connection down. Safe to call repeatedly.
func (p *Peer) Close() {
	p.closeOnce.Do(func() {
		close(p.closed)
		if err := p.pc.Close(); err != nil {
			p.cfg.Logger.Debug("close peer", "err", err)
		}
		if p.cfg.OnClose != nil {
			go p.cfg.OnClose()
		}
	})
}

// SendMotion delivers one binary motion frame if the motion channel is
// open; otherwise the frame is dropped.
func (p *Peer) SendMotion(b []byte) {
	p.mu.Lock()
	dc := p.motion
	p.mu.Unlock()
	if dc == nil {
		return
	}
	if err := dc.Send(b); err != nil {
		p.cfg.Logger.Debug("send motion", "err", err)
	}
}

func (p *Peer) onDataChannel(dc *webrtc.DataChannel) {
	if dc.Label() == MotionLabel {
		dc.OnOpen(func() {
			p.mu.Lock()
			p.motion = dc
			p.mu.Unlock()
		})
		return
	}
	if dc.Label() != EventsLabel {
		return
	}
	if p.cfg.OnMessage != nil {
		dc.OnMessage(func(m webrtc.DataChannelMessage) {
			if m.IsString && len(m.Data) <= maxClientMessage {
				p.cfg.OnMessage(m.Data)
			}
		})
	}
	dc.OnOpen(func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.dc, p.dcOpen = dc, true
		for _, b := range p.pending {
			dc.SendText(string(b))
		}
		p.pending = nil
	})
}

func (p *Peer) onTrack(remote *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
	if remote.Kind() != webrtc.RTPCodecTypeAudio {
		return
	}
	if mime := remote.Codec().MimeType; mime != webrtc.MimeTypeOpus {
		p.cfg.Logger.Warn("unsupported inbound codec", "mime", mime)
		return
	}
	dec, err := audio.NewOpusDecoder(p.cfg.InputSampleRate)
	if err != nil {
		p.cfg.Logger.Error("opus decoder", "err", err)
		return
	}
	for {
		pkt, _, err := remote.ReadRTP()
		if err != nil {
			return // track ended
		}
		if len(pkt.Payload) == 0 {
			continue
		}
		pcm, err := dec.Decode(pkt.Payload)
		if err != nil {
			p.cfg.Logger.Debug("opus decode", "err", err)
			continue
		}
		p.cfg.OnAudio(pcm)
	}
}

// drainRTCP keeps the sender's interceptors (NACK, reports) running.
func drainRTCP(s *webrtc.RTPSender) {
	buf := make([]byte, 1500)
	for {
		if _, _, err := s.Read(buf); err != nil {
			return
		}
	}
}
