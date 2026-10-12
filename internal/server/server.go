// Package server wires TalkTalk's HTTP surface: routing, middleware and
// graceful shutdown. Feature endpoints (WebRTC signaling, sessions) are
// mounted here as they land.
package server

import (
	"context"
	"errors"
	"fmt"
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

// Options configures a Server. Zero values get safe defaults.
type Options struct {
	Logger          *slog.Logger
	Version         string
	ShutdownTimeout time.Duration
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

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteOK(w, map[string]string{"status": "ok", "version": s.opts.Version})
}
