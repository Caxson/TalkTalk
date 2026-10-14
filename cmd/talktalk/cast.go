package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/Caxson/TalkTalk/internal/cast"
	"github.com/Caxson/TalkTalk/internal/config"
	"github.com/Caxson/TalkTalk/internal/server"
	"github.com/Caxson/TalkTalk/internal/session"
)

// loadCast builds the roster. With lip-sync on, only characters whose
// avatar files were downloaded are offered; without it every character is
// a voice and a persona.
func loadCast(cfg config.Config, logger *slog.Logger) (*cast.Cast, bool, error) {
	chars := cast.Builtin()
	if cfg.Avatar.CastFile != "" {
		var err error
		if chars, err = cast.LoadFile(cfg.Avatar.CastFile); err != nil {
			return nil, false, err
		}
	}
	avatars := cfg.Avatar.ModelPath != ""
	var exists cast.Exists
	if avatars {
		exists = cast.InDir(cfg.Avatar.AssetsDir)
	}
	if cfg.Photoreal.Addr == "" {
		chars = withoutVideo(chars, logger) // nothing could render them
	}
	c, err := cast.New(chars, cfg.Avatar.Character, exists)
	if err != nil {
		return nil, false, err
	}
	if avatars && len(c.List()) == 0 {
		logger.Warn("no character has its avatar files, run make assets; continuing voice-only")
		avatars = false
		if c, err = cast.New(chars, cfg.Avatar.Character, nil); err != nil {
			return nil, false, err
		}
	}
	if def := cfg.Avatar.Character; def != "" && c.Default().ID != def {
		return nil, false, fmt.Errorf("%s=%s: that character's avatar files are missing, run make assets", config.EnvCharacter, def)
	}
	for _, ch := range c.List() {
		logger.Info("character available", "id", ch.ID, "kind", ch.Kind, "voice", ch.Voice, "avatar", avatars)
	}
	return c, avatars, nil
}

// withoutVideo drops photoreal characters, for a server with no worker.
func withoutVideo(chars []cast.Character, logger *slog.Logger) []cast.Character {
	out := make([]cast.Character, 0, len(chars))
	for _, ch := range chars {
		if ch.Kind == cast.KindVideo {
			logger.Info("photoreal character off: TALKTALK_PHOTOREAL_ADDR not set", "id", ch.ID)
			continue
		}
		out = append(out, ch)
	}
	return out
}

// personaOf resolves a session's character for the session manager.
func personaOf(c *cast.Cast, basePrompt, defaultVoice string) func(string) (session.Persona, error) {
	return func(id string) (session.Persona, error) {
		ch := c.Default()
		if id != "" {
			var ok bool
			if ch, ok = c.Get(id); !ok {
				return session.Persona{}, fmt.Errorf("%w: %q", session.ErrUnknownCharacter, id)
			}
		}
		voice := ch.Voice
		if voice == "" {
			voice = defaultVoice
		}
		p := session.Persona{Character: ch.ID, Voice: voice, SystemPrompt: ch.SystemPrompt(basePrompt)}
		if ch.Kind == cast.KindVideo {
			p.Video = ch.Model
		}
		return p, nil
	}
}

// castInfo tells the browser who it can talk to and how to draw them.
func castInfo(c *cast.Cast, avatars bool, cfg config.Config, logger *slog.Logger) *server.CastInfo {
	info := &server.CastInfo{Default: c.Default().ID, Characters: []server.CharacterInfo{}}
	for _, ch := range c.List() {
		ci := server.CharacterInfo{ID: ch.ID, Name: ch.Name, Tagline: ch.Tagline, Kind: ch.Kind, Credit: ch.Credit}
		if ch.Kind == cast.KindVideo {
			ci.Background = assetURL(ch.Background) // shown until video arrives
		} else if avatars {
			ci.Renderer = assetURL(cast.RendererModule[ch.Kind])
			ci.Model = assetURL(ch.Model)
			ci.Animation = assetURL(ch.Animation)
			ci.Background = assetURL(ch.Background)
		}
		info.Characters = append(info.Characters, ci)
	}
	if avatars {
		info.Perception = perceptionInfo(cfg, logger)
	}
	return info
}

func assetURL(p string) string {
	if p == "" {
		return ""
	}
	return "/assets/" + p
}

// perceptionInfo enables in-browser face reading when MediaPipe is present.
func perceptionInfo(cfg config.Config, logger *slog.Logger) *server.PerceptionInfo {
	const dir = "vendor/mediapipe"
	for _, f := range []string{"vision_bundle.mjs", "wasm/vision_wasm_internal.wasm", "face_landmarker.task"} {
		if _, err := os.Stat(filepath.Join(cfg.Avatar.AssetsDir, dir, f)); err != nil {
			logger.Info("perception disabled: MediaPipe assets missing, run make assets", "file", f)
			return nil
		}
	}
	return &server.PerceptionInfo{
		Bundle: "/assets/" + dir + "/vision_bundle.mjs",
		Wasm:   "/assets/" + dir + "/wasm",
		Model:  "/assets/" + dir + "/face_landmarker.task",
	}
}
