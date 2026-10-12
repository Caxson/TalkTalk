package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/pion/webrtc/v4"

	"github.com/Caxson/TalkTalk/internal/httpx"
	"github.com/Caxson/TalkTalk/internal/session"
)

type fakeSessions struct {
	gotOffer webrtc.SessionDescription
	err      error
	closed   []string
}

func (f *fakeSessions) Start(_ context.Context, offer webrtc.SessionDescription) (string, webrtc.SessionDescription, error) {
	f.gotOffer = offer
	if f.err != nil {
		return "", webrtc.SessionDescription{}, f.err
	}
	return "sess-1", webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: "v=0 answer"}, nil
}

func (f *fakeSessions) Close(id string) bool {
	f.closed = append(f.closed, id)
	return id == "sess-1"
}

func do(t *testing.T, h http.Handler, method, path, body string) (*httptest.ResponseRecorder, httpx.Envelope) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	var env httpx.Envelope
	json.Unmarshal(rec.Body.Bytes(), &env)
	return rec, env
}

func TestCreateSession(t *testing.T) {
	fs := &fakeSessions{}
	h := New(Options{Logger: discardLogger(), Sessions: fs}).Handler()
	rec, env := do(t, h, http.MethodPost, "/api/sessions", `{"type":"offer","sdp":"v=0 offer"}`)
	if rec.Code != http.StatusCreated || !env.Success {
		t.Fatalf("status %d env %+v", rec.Code, env)
	}
	data := env.Data.(map[string]any)
	if data["id"] != "sess-1" || data["type"] != "answer" || data["sdp"] != "v=0 answer" {
		t.Errorf("data = %v", data)
	}
	if fs.gotOffer.Type != webrtc.SDPTypeOffer || fs.gotOffer.SDP != "v=0 offer" {
		t.Errorf("offer = %+v", fs.gotOffer)
	}
}

func TestCreateSessionRejectsBadBodies(t *testing.T) {
	h := New(Options{Logger: discardLogger(), Sessions: &fakeSessions{}}).Handler()
	for _, body := range []string{
		``, `not json`, `{"type":"answer","sdp":"x"}`, `{"type":"offer","sdp":""}`,
		`{"type":"offer","sdp":"x","extra":1}`,
		`{"type":"offer","sdp":"` + strings.Repeat("a", 70<<10) + `"}`,
	} {
		rec, env := do(t, h, http.MethodPost, "/api/sessions", body)
		if rec.Code != http.StatusBadRequest || env.Success || env.Error == nil {
			t.Errorf("body %.30q: status %d env %+v", body, rec.Code, env)
		}
	}
}

func TestCreateSessionMapsErrors(t *testing.T) {
	cases := []struct {
		err    error
		status int
	}{
		{session.ErrCapacity, http.StatusServiceUnavailable},
		{errors.New("rtc: set offer: bad sdp"), http.StatusBadRequest},
	}
	for _, c := range cases {
		h := New(Options{Logger: discardLogger(), Sessions: &fakeSessions{err: c.err}}).Handler()
		rec, env := do(t, h, http.MethodPost, "/api/sessions", `{"type":"offer","sdp":"x"}`)
		if rec.Code != c.status || env.Success {
			t.Errorf("err %v: status %d, want %d", c.err, rec.Code, c.status)
		}
		if env.Error != nil && strings.Contains(*env.Error, "bad sdp") {
			t.Errorf("internal error text leaked: %q", *env.Error)
		}
	}
}

func TestDeleteSession(t *testing.T) {
	fs := &fakeSessions{}
	h := New(Options{Logger: discardLogger(), Sessions: fs}).Handler()
	if rec, _ := do(t, h, http.MethodDelete, "/api/sessions/sess-1", ""); rec.Code != http.StatusOK {
		t.Errorf("delete live: %d", rec.Code)
	}
	if rec, _ := do(t, h, http.MethodDelete, "/api/sessions/nope", ""); rec.Code != http.StatusNotFound {
		t.Errorf("delete unknown: %d", rec.Code)
	}
}

func TestSessionRoutesAbsentWithoutService(t *testing.T) {
	h := New(Options{Logger: discardLogger()}).Handler()
	if rec, _ := do(t, h, http.MethodPost, "/api/sessions", `{}`); rec.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404 when sessions disabled", rec.Code)
	}
}

func TestStaticFiles(t *testing.T) {
	static := fstest.MapFS{
		"index.html": {Data: []byte("<!doctype html><title>TalkTalk</title>")},
		"app.js":     {Data: []byte("console.log(1)")},
	}
	h := New(Options{Logger: discardLogger(), Static: static}).Handler()
	rec, _ := do(t, h, http.MethodGet, "/", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "TalkTalk") {
		t.Errorf("GET / = %d %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q", got)
	}
	if rec, _ := do(t, h, http.MethodGet, "/app.js", ""); rec.Code != http.StatusOK {
		t.Errorf("GET /app.js = %d", rec.Code)
	}
	// API 404s keep the envelope even with static files mounted.
	if rec, env := do(t, h, http.MethodGet, "/api/nope", ""); rec.Code != http.StatusNotFound || env.Error == nil {
		t.Errorf("GET /api/nope = %d %+v", rec.Code, env)
	}
	if rec, _ := do(t, h, http.MethodGet, "/healthz", ""); rec.Code != http.StatusOK {
		t.Errorf("healthz shadowed by static: %d", rec.Code)
	}
}
