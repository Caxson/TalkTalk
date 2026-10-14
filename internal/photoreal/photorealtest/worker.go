// Package photorealtest provides a fake photoreal worker for tests: it
// answers START with READY and sends one small "frame" per 640 samples of
// audio it receives, numbered like the real worker's.
package photorealtest

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"sync"
	"testing"
)

// Worker is a fake worker listening on Addr.
type Worker struct {
	Addr string
	ln   net.Listener

	mu         sync.Mutex
	characters []string
	samples    int
}

// New starts a fake worker, stopped at test cleanup.
func New(t testing.TB) *Worker {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	w := &Worker{Addr: ln.Addr().String(), ln: ln}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go w.serve(conn)
		}
	}()
	return w
}

// Characters lists the characters asked for, in order.
func (w *Worker) Characters() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.characters...)
}

// Samples is the audio received so far, at 16 kHz.
func (w *Worker) Samples() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.samples
}

// AccessUnit is the fake frame: an Annex B IDR slice, which an RTP H.264
// packetizer accepts.
var AccessUnit = append([]byte{0, 0, 0, 1, 0x65}, make([]byte, 64)...)

func (w *Worker) serve(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	var hdr [5]byte
	sent := 0
	for {
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return
		}
		payload := make([]byte, binary.BigEndian.Uint32(hdr[1:]))
		if _, err := io.ReadFull(r, payload); err != nil {
			return
		}
		switch hdr[0] {
		case 'S':
			var start struct{ Character string }
			json.Unmarshal(payload, &start)
			w.mu.Lock()
			w.characters = append(w.characters, start.Character)
			w.mu.Unlock()
			conn.Write(msg('R', []byte(`{"width":720,"height":1280,"fps":25,"first_frame":0}`)))
		case 'A':
			w.mu.Lock()
			w.samples += len(payload) / 2
			due := w.samples / 640
			w.mu.Unlock()
			for ; sent < due; sent++ {
				p := make([]byte, 4, 4+len(AccessUnit))
				binary.BigEndian.PutUint32(p, uint32(sent))
				conn.Write(msg('V', append(p, AccessUnit...)))
			}
		case 'X':
			return
		}
	}
}

func msg(kind byte, payload []byte) []byte {
	b := make([]byte, 5+len(payload))
	b[0] = kind
	binary.BigEndian.PutUint32(b[1:], uint32(len(payload)))
	copy(b[5:], payload)
	return b
}
