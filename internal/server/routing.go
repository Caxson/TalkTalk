package server

import (
	"net/http"
	"net/http/httptest"

	"github.com/Caxson/TalkTalk/internal/httpx"
)

// envelopeMuxErrors makes the mux's own 404/405 responses use the JSON
// envelope like every other endpoint, while keeping the mux's routing
// semantics (including the Allow header on 405).
func envelopeMuxErrors(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, pattern := mux.Handler(r)
		if pattern != "" {
			h.ServeHTTP(w, r)
			return
		}
		// No route matched: let the mux decide 404 vs 405, then re-render.
		probe := httptest.NewRecorder()
		h.ServeHTTP(probe, r)
		if allow := probe.Header().Get("Allow"); allow != "" {
			w.Header().Set("Allow", allow)
		}
		httpx.WriteError(w, probe.Code, http.StatusText(probe.Code))
	})
}
