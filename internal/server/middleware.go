package server

import (
	"net/http"
	"runtime/debug"
	"time"

	"github.com/Caxson/TalkTalk/internal/httpx"
)

// wrap applies the middleware chain, outermost first.
func (s *Server) wrap(h http.Handler) http.Handler {
	return s.logRequests(s.recoverPanics(h))
}

func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.opts.Logger.Error("panic in handler",
					"panic", rec, "method", r.Method, "path", r.URL.Path,
					"stack", string(debug.Stack()))
				httpx.WriteError(w, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		s.opts.Logger.Debug("http request",
			"method", r.Method, "path", r.URL.Path,
			"status", rw.status, "duration", time.Since(start))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach the underlying writer
// (needed later for flushing and hijacking).
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
