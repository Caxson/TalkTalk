// Package photoreal talks to the GPU worker that renders photoreal
// characters (worker/photoreal): it streams the audio the listener will
// hear, ahead of playback, and receives H.264 frames numbered by that audio.
//
// Wire format (see worker/photoreal/protocol.py): each message is a 1-byte
// type, a 4-byte big-endian length and a payload. Frame k shows the worker's
// audio samples [k*640, (k+1)*640) at 16 kHz, i.e. 40 ms per frame counted
// from the first sample sent.
package photoreal

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/Caxson/TalkTalk/internal/audio"
)

// SampleRate is the audio rate the worker expects.
const SampleRate = 16000

const (
	msgStart = 'S'
	msgAudio = 'A'
	msgStop  = 'X'
	msgReady = 'R'
	msgVideo = 'V'
	msgError = 'E'

	maxPayload = 8 << 20
	// sendBacklog bounds audio waiting for the network: 30 s of 20 ms
	// chunks. Audio is never dropped (the worker numbers frames by it);
	// a backlog this deep means the link is gone.
	sendBacklog = 1500
	dialTimeout = 2 * time.Second
	// stopGrace is how long Close lets the sender say STOP before cutting
	// the connection: a stalled link (a hung SSH tunnel) must not hold a
	// session's cleanup.
	stopGrace = 300 * time.Millisecond
)

// Info is what the worker reports once it renders.
type Info struct {
	Width      int   `json:"width"`
	Height     int   `json:"height"`
	FPS        int   `json:"fps"`
	FirstFrame int64 `json:"first_frame"`
}

// Handlers receive the worker's output. Called from one goroutine.
type Handlers struct {
	OnReady func(Info)
	OnFrame func(index int64, accessUnit []byte)
}

// Client is one conversation's link to the worker.
type Client struct {
	conn net.Conn
	rs   *audio.Resampler
	out  chan []byte
	log  *slog.Logger

	closeOnce sync.Once
	closed    chan struct{}
	broken    chan struct{} // the send queue overflowed
	sendDone  chan struct{} // the sender, which owns all writes, has quit
}

// Dial connects to the worker at addr and asks it to render character.
// inRate is the rate of the audio that will be fed.
func Dial(ctx context.Context, addr, character string, inRate int, log *slog.Logger) (*Client, error) {
	rs, err := audio.NewResampler(inRate, SampleRate)
	if err != nil {
		return nil, err
	}
	d := net.Dialer{Timeout: dialTimeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("photoreal: dial %s: %w", addr, err)
	}
	start, _ := json.Marshal(map[string]string{"character": character})
	if _, err := conn.Write(frame(msgStart, start)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("photoreal: start: %w", err)
	}
	if log == nil {
		log = slog.Default()
	}
	c := &Client{conn: conn, rs: rs, out: make(chan []byte, sendBacklog), log: log,
		closed: make(chan struct{}), broken: make(chan struct{}), sendDone: make(chan struct{})}
	go c.send()
	return c, nil
}

// Feed queues audio for the worker. It never blocks (it runs on the
// playout clock); if the link cannot keep up the client gives up, since
// skipping audio would misnumber every later frame.
func (c *Client) Feed(pcm []int16) {
	msg := frame(msgAudio, audio.Int16ToBytes(c.rs.Process(pcm)))
	select {
	case c.out <- msg:
	case <-c.closed:
	default:
		c.closeOnce.Do(func() {
			close(c.broken)
			close(c.closed)
		})
	}
}

// send owns every write on the connection, so messages never interleave;
// on close it says STOP and hangs up.
func (c *Client) send() {
	defer close(c.sendDone)
	defer c.conn.Close()
	w := bufio.NewWriterSize(c.conn, 64<<10)
	for {
		select {
		case msg := <-c.out:
			_, err := w.Write(msg)
			// Flush once the queue is drained: latency matters, but there is
			// no point in a syscall per 20 ms chunk when several are waiting.
			if err == nil && len(c.out) == 0 {
				err = w.Flush()
			}
			if err != nil {
				c.closeOnce.Do(func() { close(c.closed) })
				return
			}
		case <-c.closed:
			_ = c.conn.SetWriteDeadline(time.Now().Add(200 * time.Millisecond))
			_, _ = w.Write(frame(msgStop, nil))
			_ = w.Flush()
			return
		}
	}
}

// Run reads the worker's messages until the connection ends or ctx is done.
func (c *Client) Run(ctx context.Context, h Handlers) error {
	stop := context.AfterFunc(ctx, c.Close)
	defer stop()
	r := bufio.NewReaderSize(c.conn, 256<<10)
	var hdr [5]byte
	for {
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return c.endErr(err)
		}
		n := binary.BigEndian.Uint32(hdr[1:])
		if n > maxPayload {
			return fmt.Errorf("photoreal: message of %d bytes", n)
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(r, payload); err != nil {
			return c.endErr(err)
		}
		switch hdr[0] {
		case msgReady:
			var info Info
			if err := json.Unmarshal(payload, &info); err != nil {
				return fmt.Errorf("photoreal: ready: %w", err)
			}
			if h.OnReady != nil {
				h.OnReady(info)
			}
		case msgVideo:
			if len(payload) < 4 {
				return errors.New("photoreal: short video message")
			}
			if h.OnFrame != nil {
				h.OnFrame(int64(binary.BigEndian.Uint32(payload)), payload[4:])
			}
		case msgError:
			var e struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(payload, &e)
			return fmt.Errorf("photoreal: worker: %s", e.Error)
		}
	}
}

func (c *Client) endErr(err error) error {
	select {
	case <-c.broken:
		return errors.New("photoreal: worker link too slow, audio backlog overflowed")
	case <-c.closed:
		return nil
	default:
	}
	if errors.Is(err, io.EOF) {
		return errors.New("photoreal: worker closed the connection")
	}
	return fmt.Errorf("photoreal: read: %w", err)
}

// Close tells the worker to stop and drops the connection.
func (c *Client) Close() {
	c.closeOnce.Do(func() { close(c.closed) })
	select {
	case <-c.sendDone:
	case <-time.After(stopGrace):
		// The sender is stuck in a write: unblock it.
		_ = c.conn.SetDeadline(time.Now())
		<-c.sendDone
	}
}

func frame(kind byte, payload []byte) []byte {
	b := make([]byte, 5+len(payload))
	b[0] = kind
	binary.BigEndian.PutUint32(b[1:], uint32(len(payload)))
	copy(b[5:], payload)
	return b
}
