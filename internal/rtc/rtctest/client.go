// Package rtctest provides an in-process WebRTC client that behaves like
// the TalkTalk browser page, for tests.
package rtctest

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"

	"github.com/Caxson/TalkTalk/internal/audio"
)

// Client sends a microphone track, opens the events channel, and counts
// downlink audio.
type Client struct {
	PC       *webrtc.PeerConnection
	mic      *webrtc.TrackLocalStaticSample
	enc      *audio.OpusEncoder
	Events   chan map[string]any
	DCOpened chan struct{}

	mu   sync.Mutex
	pkts int
}

// New creates a client closed at test cleanup.
func New(t testing.TB) *Client {
	t.Helper()
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	c := &Client{PC: pc, Events: make(chan map[string]any, 256), DCOpened: make(chan struct{})}
	c.enc, err = audio.NewOpusEncoder(48000)
	if err != nil {
		t.Fatal(err)
	}
	c.mic, err = webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, "audio", "mic")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pc.AddTransceiverFromTrack(c.mic, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendrecv}); err != nil {
		t.Fatal(err)
	}
	pc.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		for {
			pkt, _, err := tr.ReadRTP()
			if err != nil {
				return
			}
			if len(pkt.Payload) > 0 {
				c.mu.Lock()
				c.pkts++
				c.mu.Unlock()
			}
		}
	})
	dc, err := pc.CreateDataChannel("events", nil)
	if err != nil {
		t.Fatal(err)
	}
	dc.OnOpen(func() { close(c.DCOpened) })
	dc.OnMessage(func(m webrtc.DataChannelMessage) {
		var v map[string]any
		if json.Unmarshal(m.Data, &v) == nil {
			select {
			case c.Events <- v:
			default:
			}
		}
	})
	return c
}

// Offer returns a complete offer (ICE gathered).
func (c *Client) Offer(t testing.TB) webrtc.SessionDescription {
	t.Helper()
	offer, err := c.PC.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gathered := webrtc.GatheringCompletePromise(c.PC)
	if err := c.PC.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	<-gathered
	return *c.PC.LocalDescription()
}

// Accept applies the server's answer.
func (c *Client) Accept(t testing.TB, answer webrtc.SessionDescription) {
	t.Helper()
	if err := c.PC.SetRemoteDescription(answer); err != nil {
		t.Fatal(err)
	}
}

// Speak streams pcm (48 kHz mono) in real time as 20ms Opus frames.
func (c *Client) Speak(pcm []int16) error {
	for off := 0; off+960 <= len(pcm); off += 960 {
		pkt, err := c.enc.Encode(pcm[off : off+960])
		if err != nil {
			return err
		}
		if err := c.mic.WriteSample(media.Sample{Data: pkt, Duration: 20 * time.Millisecond}); err != nil {
			return err
		}
		time.Sleep(20 * time.Millisecond)
	}
	return nil
}

// Packets is the number of downlink audio packets received.
func (c *Client) Packets() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pkts
}
