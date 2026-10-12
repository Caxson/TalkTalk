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
	"github.com/Caxson/TalkTalk/internal/web"
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

	sessions, err := newSessions(cfg, logger)
	if err != nil {
		return err
	}

	ln, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.HTTPAddr, err)
	}

	logger.Info("talktalk starting", "version", version.Version,
		"llm", cfg.LLM.Model, "asr", cfg.Speech.ASRModel, "tts", cfg.Speech.TTSModel+"/"+cfg.Speech.TTSVoice)
	srv := server.New(server.Options{
		Logger:          logger,
		Version:         version.Version,
		ShutdownTimeout: cfg.ShutdownTimeout,
		Sessions:        sessions,
		Static:          web.Static(),
	})
	serveErr := srv.Serve(ctx, ln)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	sessions.CloseAll(shutdownCtx)
	if serveErr != nil {
		return serveErr
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
