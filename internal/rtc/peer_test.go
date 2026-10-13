package rtc

import (
	"context"
	"io"
	"log/slog"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/Caxson/TalkTalk/internal/audio"
	"github.com/Caxson/TalkTalk/internal/rtc/rtctest"
)

func TestPeerAudioBothWaysAndEvents(t *testing.T) {
	c := rtctest.New(t)

	var mu sync.Mutex
	var received []int16
	closed := make(chan struct{})
	msgs := make(chan string, 4)
	peer, answer, err := Answer(context.Background(), c.Offer(t), Config{
		InputSampleRate: 16000,
		OnAudio: func(pcm []int16) {
			mu.Lock()
			received = append(received, pcm...)
			mu.Unlock()
		},
		OnClose:   func() { close(closed) },
		OnMessage: func(b []byte) { msgs <- string(b) },
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Queued before the channel opens; must still arrive.
	peer.Send(map[string]any{"type": "state", "state": "listening"})
	c.Accept(t, answer)

	select {
	case <-c.DCOpened:
	case <-time.After(5 * time.Second):
		t.Fatal("data channel did not open")
	}
	select {
	case ev := <-c.Events:
		if ev["state"] != "listening" {
			t.Errorf("event = %v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("queued event not delivered")
	}

	// Client → server messages, with an oversized one dropped.
	c.DC.SendText(`{"type":"perception","face":true,"mouth":false}`)
	c.DC.SendText(strings.Repeat("x", 2000))
	c.DC.SendText(`{"type":"ping"}`)
	for _, want := range []string{`{"type":"perception","face":true,"mouth":false}`, `{"type":"ping"}`} {
		select {
		case m := <-msgs:
			if m != want {
				t.Errorf("client message = %.40q, want %q", m, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("client message not delivered")
		}
	}

	// Uplink: 1s of a 440 Hz tone from the "browser" microphone.
	tone := make([]int16, 48000)
	for j := range tone {
		tone[j] = int16(8000 * math.Sin(2*math.Pi*440*float64(j)/48000))
	}
	go c.Speak(tone)

	// Downlink: server speaks.
	srvEnc, _ := audio.NewOpusEncoder(24000)
	go func() {
		for i := 0; i < 50; i++ {
			pkt, _ := srvEnc.Encode(make([]int16, 480))
			if peer.WriteAudio(pkt, 20*time.Millisecond) != nil {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()

	for deadline := time.Now().Add(5 * time.Second); c.Packets() < 25; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("client received no downlink audio")
		}
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(received)
		mu.Unlock()
		if n >= 12000 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	got := append([]int16(nil), received...)
	mu.Unlock()
	if len(got) < 12000 {
		t.Fatalf("server decoded %d samples, want ≥ 0.75s at 16k", len(got))
	}
	if lvl := audio.RMSdBFS(got[len(got)/2:]); lvl < -20 || lvl > -8 {
		t.Errorf("uplink level %.1f dBFS, want about -15", lvl)
	}

	c.PC.Close()
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("OnClose not called after client hung up")
	}
	if err := peer.WriteAudio([]byte{1}, 20*time.Millisecond); err == nil {
		t.Error("WriteAudio after close succeeded")
	}
}

func TestAnswerRejectsBadInput(t *testing.T) {
	cfg := Config{InputSampleRate: 16000, OnAudio: func([]int16) {}}
	if _, _, err := Answer(context.Background(), webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: "x"}, cfg); err == nil {
		t.Error("accepted an answer as offer")
	}
	if _, _, err := Answer(context.Background(), webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: "garbage"}, cfg); err == nil {
		t.Error("accepted garbage SDP")
	}
	if _, _, err := Answer(context.Background(), webrtc.SessionDescription{Type: webrtc.SDPTypeOffer}, Config{}); err == nil {
		t.Error("accepted empty config")
	}
}
