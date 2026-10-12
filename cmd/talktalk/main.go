// Command talktalk runs the TalkTalk control plane.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/Caxson/TalkTalk/internal/config"
	"github.com/Caxson/TalkTalk/internal/server"
	"github.com/Caxson/TalkTalk/internal/version"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "talktalk:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.LookupEnv)
	if err != nil {
		return err
	}
	logger := newLogger(cfg)
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ln, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.HTTPAddr, err)
	}

	logger.Info("talktalk starting", "version", version.Version)
	srv := server.New(server.Options{
		Logger:          logger,
		Version:         version.Version,
		ShutdownTimeout: cfg.ShutdownTimeout,
	})
	if err := srv.Serve(ctx, ln); err != nil {
		return err
	}
	logger.Info("talktalk stopped")
	return nil
}

func newLogger(cfg config.Config) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}
	if cfg.LogFormat == config.LogFormatJSON {
		return slog.New(slog.NewJSONHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, opts))
}
