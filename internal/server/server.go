// Package server wires TalkTalk's HTTP surface: routing, middleware and
// graceful shutdown. Feature endpoints (WebRTC signaling, sessions) are
// mounted here as they land.
package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/Caxson/TalkTalk/internal/httpx"
)

const (
	readHeaderTimeout      = 5 * time.Second
	idleTimeout            = 60 * time.Second
	defaultShutdownTimeout = 10 * time.Second
)

// Options configures a Server. Zero values get safe defaults; nil
// Sessions or Static leave those routes unmounted.
type Options struct {
	Logger          *slog.Logger
	Version         string
	ShutdownTimeout time.Duration
	Sessions        SessionService
	Static          fs.FS
	// Assets serves downloaded client assets under /assets/ (nil: none).
	Assets fs.FS
	// Cast lists the characters a conversation can be held with; nil
	// means a single unnamed assistant without an avatar.
	Cast *CastInfo
}

// CastInfo is what GET /api/characters returns.
type CastInfo struct {
	Default    string          `json:"default"`
	Characters []CharacterInfo `json:"characters"`
	// Perception, if set, lets the browser read the user's face (locally).
	Perception *PerceptionInfo `json:"perception,omitempty"`
}

// CharacterInfo describes one character and, when avatars are enabled,
// which renderer draws it and where its files are.
type CharacterInfo struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Tagline    string `json:"tagline,omitempty"`
	Kind       string `json:"kind"`                 // "vrm" | "lam" | "live2d"
	Renderer   string `json:"renderer,omitempty"`   // ES module URL of the renderer library
	Model      string `json:"model,omitempty"`      // avatar file URL
	Animation  string `json:"animation,omitempty"`  // idle motion (.vrma), VRM only
	Background string `json:"background,omitempty"` // photo the head is composited onto (LAM)
	Credit     string `json:"credit,omitempty"`     // attribution the avatar's license requires
}

// PerceptionInfo locates MediaPipe's face landmarker for the browser.
type PerceptionInfo struct {
	Bundle string `json:"bundle"` // tasks-vision ES module
	Wasm   string `json:"wasm"`   // directory of the WASM runtime
	Model  string `json:"model"`  // face_landmarker.task
}

// Server is TalkTalk's HTTP server.
type Server struct {
	opts    Options
	handler http.Handler
}

// New builds a Server with all routes registered.
func New(opts Options) *Server {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.ShutdownTimeout <= 0 {
		opts.ShutdownTimeout = defaultShutdownTimeout
	}
	s := &Server{opts: opts}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	if opts.Sessions != nil {
		mux.HandleFunc("POST /api/sessions", s.handleCreateSession)
		mux.HandleFunc("DELETE /api/sessions/{id}", s.handleDeleteSession)
	}
	mux.HandleFunc("GET /api/characters", s.handleCharacters)
	if opts.Assets != nil {
		assets := http.StripPrefix("/assets/", http.FileServerFS(opts.Assets))
		mux.Handle("GET /assets/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Versioned, multi-megabyte, immutable for a given download.
			w.Header().Set("Cache-Control", "public, max-age=86400")
			assets.ServeHTTP(w, r)
		}))
	}
	if opts.Static != nil {
		files := http.FileServerFS(opts.Static)
		mux.Handle("GET /", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// The page is small and changes with every build.
			w.Header().Set("Cache-Control", "no-cache")
			files.ServeHTTP(w, r)
		}))
		// Keep JSON errors for the API namespace.
		mux.HandleFunc("GET /api/", func(w http.ResponseWriter, _ *http.Request) {
			httpx.WriteError(w, http.StatusNotFound, http.StatusText(http.StatusNotFound))
		})
	}
	s.handler = s.wrap(envelopeMuxErrors(mux))
	return s
}

// Handler returns the fully wrapped root handler.
func (s *Server) Handler() http.Handler { return s.handler }

// Serve accepts connections on ln until ctx is cancelled, then drains
// in-flight requests within ShutdownTimeout. It returns nil on a clean stop.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	hs := &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
		ErrorLog:          slog.NewLogLogger(s.opts.Logger.Handler(), slog.LevelWarn),
	}

	errCh := make(chan error, 1)
	go func() { errCh <- hs.Serve(ln) }()
	s.opts.Logger.Info("http server listening", "addr", ln.Addr().String())

	select {
	case err := <-errCh:
		return fmt.Errorf("http serve: %w", err)
	case <-ctx.Done():
	}

	s.opts.Logger.Info("http server shutting down", "timeout", s.opts.ShutdownTimeout)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), s.opts.ShutdownTimeout)
	defer cancel()
	if err := hs.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("http shutdown: %w", err)
	}
	if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("http serve: %w", err)
	}
	return nil
}

func (s *Server) handleCharacters(w http.ResponseWriter, _ *http.Request) {
	if s.opts.Cast == nil {
		httpx.WriteOK(w, CastInfo{Characters: []CharacterInfo{}})
		return
	}
	httpx.WriteOK(w, s.opts.Cast)
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteOK(w, map[string]string{"status": "ok", "version": s.opts.Version})
}
