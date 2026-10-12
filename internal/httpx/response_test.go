package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func decode(t *testing.T, rec *httptest.ResponseRecorder) Envelope {
	t.Helper()
	var env Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("body is not a JSON envelope: %v (%s)", err, rec.Body.String())
	}
	return env
}

func TestWriteOK(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteOK(rec, map[string]string{"k": "v"})

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	env := decode(t, rec)
	if !env.Success || env.Error != nil {
		t.Errorf("envelope = %+v, want success with nil error", env)
	}
	data, ok := env.Data.(map[string]any)
	if !ok || data["k"] != "v" {
		t.Errorf("data = %#v", env.Data)
	}
}

func TestWriteError(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, http.StatusNotFound, "not found")

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	env := decode(t, rec)
	if env.Success || env.Data != nil {
		t.Errorf("envelope = %+v, want failure with nil data", env)
	}
	if env.Error == nil || *env.Error != "not found" {
		t.Errorf("error = %v, want \"not found\"", env.Error)
	}
}
