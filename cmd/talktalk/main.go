// Command talktalk runs the TalkTalk control plane.
package main

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
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

	// Only the browser-facing assets are served, and only with an avatar.
	avatar := avatarInfo(cfg, logger)
	var assets fs.FS
	if avatar != nil {
		assets = server.PublicFS(os.DirFS(cfg.Avatar.AssetsDir), "vendor", "avatars")
	}

	logger.Info("talktalk starting", "version", version.Version,
		"llm", cfg.LLM.Model, "asr", cfg.Speech.ASRModel, "tts", cfg.Speech.TTSModel+"/"+cfg.Speech.TTSVoice)
	srv := server.New(server.Options{
		Logger:          logger,
		Version:         version.Version,
		ShutdownTimeout: cfg.ShutdownTimeout,
		Sessions:        sessions,
		Static:          web.Static(),
		Assets:          assets,
		Avatar:          avatar,
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

// avatarInfo enables the browser avatar when lip-sync is configured and
// the chosen renderer's downloaded files are present.
func avatarInfo(cfg config.Config, logger *slog.Logger) *server.AvatarInfo {
	if cfg.Avatar.ModelPath == "" {
		return nil
	}
	info := &server.AvatarInfo{Kind: cfg.Avatar.Kind}
	var files []string
	switch cfg.Avatar.Kind {
	case "lam":
		info.Renderer = "vendor/gaussian-splat-renderer-for-lam.module.js"
		info.Model = "avatars/p2-1.zip"
		files = []string{info.Renderer, info.Model}
	default: // vrm
		info.Renderer = "vendor/three-vrm.module.min.js"
		info.Model = "avatars/AvatarSample_B.vrm"
		info.Animation = "avatars/idle_loop.vrma"
		files = []string{info.Renderer, info.Model, info.Animation,
			"vendor/three-vrm-animation.module.min.js", "vendor/three/three.module.js"}
	}
	for _, f := range files {
		if _, err := os.Stat(filepath.Join(cfg.Avatar.AssetsDir, f)); err != nil {
			logger.Warn("avatar assets missing, run make assets", "kind", info.Kind, "file", f)
			return nil
		}
	}
	for _, p := range []*string{&info.Renderer, &info.Model, &info.Animation} {
		if *p != "" {
			*p = "/assets/" + *p
		}
	}
	logger.Info("avatar enabled", "kind", info.Kind, "model", info.Model)
	return info
}
