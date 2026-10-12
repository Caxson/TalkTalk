// Package httpx holds HTTP helpers shared by every TalkTalk endpoint.
package httpx

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// Envelope is the uniform JSON shape of every API response.
type Envelope struct {
	Success bool    `json:"success"`
	Data    any     `json:"data"`
	Error   *string `json:"error"`
}

// WriteOK writes a 200 envelope carrying data.
func WriteOK(w http.ResponseWriter, data any) {
	write(w, http.StatusOK, Envelope{Success: true, Data: data})
}

// WriteError writes a failure envelope. msg must be safe to show to clients.
func WriteError(w http.ResponseWriter, status int, msg string) {
	write(w, status, Envelope{Error: &msg})
}

func write(w http.ResponseWriter, status int, env Envelope) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(env); err != nil {
		slog.Error("encode response", "err", err)
	}
}
