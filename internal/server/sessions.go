package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/pion/webrtc/v4"

	"github.com/Caxson/TalkTalk/internal/httpx"
	"github.com/Caxson/TalkTalk/internal/session"
)

// maxOfferBytes bounds the SDP request body; real offers are a few KB.
const maxOfferBytes = 64 << 10

// SessionService starts and stops conversations (session.Manager).
type SessionService interface {
	Start(ctx context.Context, offer webrtc.SessionDescription) (id string, answer webrtc.SessionDescription, err error)
	Close(id string) bool
}

type sdpBody struct {
	Type string `json:"type"`
	SDP  string `json:"sdp"`
}

type sessionCreated struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	SDP  string `json:"sdp"`
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxOfferBytes))
	dec.DisallowUnknownFields()
	var body sdpBody
	if err := dec.Decode(&body); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, `body must be {"type":"offer","sdp":"..."} under 64KB`)
		return
	}
	if body.Type != "offer" || body.SDP == "" {
		httpx.WriteError(w, http.StatusBadRequest, "type must be offer and sdp must be non-empty")
		return
	}

	id, answer, err := s.opts.Sessions.Start(r.Context(), webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: body.SDP})
	switch {
	case errors.Is(err, session.ErrCapacity):
		httpx.WriteError(w, http.StatusServiceUnavailable, "all conversation slots are busy, try again later")
		return
	case err != nil:
		s.opts.Logger.Warn("session negotiation failed", "err", err)
		httpx.WriteError(w, http.StatusBadRequest, "could not negotiate a WebRTC session from this offer")
		return
	}
	w.Header().Set("Location", "/api/sessions/"+id)
	httpx.Write(w, http.StatusCreated, sessionCreated{ID: id, Type: answer.Type.String(), SDP: answer.SDP})
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	if !s.opts.Sessions.Close(r.PathValue("id")) {
		httpx.WriteError(w, http.StatusNotFound, "no such session")
		return
	}
	httpx.WriteOK(w, map[string]bool{"closed": true})
}
