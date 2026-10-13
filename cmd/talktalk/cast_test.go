package main

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/Caxson/TalkTalk/internal/cast"
	"github.com/Caxson/TalkTalk/internal/config"
	"github.com/Caxson/TalkTalk/internal/session"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func roster(t *testing.T) *cast.Cast {
	t.Helper()
	c, err := cast.New([]cast.Character{
		{ID: "mio", Name: "Mio", Kind: cast.KindVRM, Model: "avatars/m.vrm", Voice: "v-mio", Persona: "我是 Mio。"},
		{ID: "ya", Name: "Ya", Kind: cast.KindLAM, Model: "avatars/y.zip", Credit: "by someone"},
	}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestPersonaOf(t *testing.T) {
	persona := personaOf(roster(t), "BASE", "v-default")
	p, err := persona("")
	if err != nil || p.Character != "mio" || p.Voice != "v-mio" || p.SystemPrompt != "BASE\n我是 Mio。" {
		t.Errorf("default persona = %+v, %v", p, err)
	}
	p, err = persona("ya")
	if err != nil || p.Voice != "v-default" || p.SystemPrompt != "BASE" {
		t.Errorf("ya = %+v, %v (no voice: the configured default)", p, err)
	}
	if _, err := persona("ghost"); !errors.Is(err, session.ErrUnknownCharacter) {
		t.Errorf("unknown character: %v", err)
	}
}

func TestCastInfo(t *testing.T) {
	var cfg config.Config
	cfg.Avatar.AssetsDir = t.TempDir()
	info := castInfo(roster(t), true, cfg, quiet)
	if info.Default != "mio" || len(info.Characters) != 2 {
		t.Fatalf("info = %+v", info)
	}
	m, y := info.Characters[0], info.Characters[1]
	if m.Model != "/assets/avatars/m.vrm" || m.Renderer != "/assets/vendor/three-vrm.module.min.js" || m.Animation != "" {
		t.Errorf("mio = %+v", m)
	}
	if y.Credit != "by someone" || y.Renderer != "/assets/vendor/gaussian-splat-renderer-for-lam.module.js" {
		t.Errorf("ya = %+v", y)
	}
	if info.Perception != nil {
		t.Error("perception enabled without MediaPipe files")
	}
	voice := castInfo(roster(t), false, cfg, quiet)
	if voice.Characters[0].Model != "" || voice.Characters[0].Renderer != "" {
		t.Errorf("voice-only character carries avatar files: %+v", voice.Characters[0])
	}
}

func TestLoadCast(t *testing.T) {
	dir := t.TempDir()
	var cfg config.Config
	cfg.Avatar.AssetsDir = dir
	cfg.Avatar.ModelPath = "lips.onnx"

	// Avatars on, nothing downloaded: voice-only, everyone offered.
	c, avatars, err := loadCast(cfg, quiet)
	if err != nil || avatars || len(c.List()) != len(cast.Builtin()) {
		t.Fatalf("no assets: avatars=%v n=%d err=%v", avatars, len(c.List()), err)
	}

	// A custom roster whose files exist.
	for _, f := range []string{"avatars/k.vrm", "vendor/three-vrm.module.min.js", "vendor/three-vrm-animation.module.min.js", "vendor/three/three.module.js"} {
		os.MkdirAll(filepath.Join(dir, filepath.Dir(f)), 0o755)
		os.WriteFile(filepath.Join(dir, f), nil, 0o644)
	}
	cfg.Avatar.CastFile = filepath.Join(dir, "cast.json")
	os.WriteFile(cfg.Avatar.CastFile, []byte(`[{"id":"kiki","name":"Kiki","kind":"vrm","model":"avatars/k.vrm"},{"id":"gone","name":"Gone","kind":"vrm","model":"avatars/x.vrm"}]`), 0o644)
	c, avatars, err = loadCast(cfg, quiet)
	if err != nil || !avatars || len(c.List()) != 1 || c.Default().ID != "kiki" {
		t.Fatalf("cast file: avatars=%v list=%+v err=%v", avatars, c.List(), err)
	}

	// A default whose files are missing is a startup error, not a surprise.
	cfg.Avatar.Character = "gone"
	if _, _, err := loadCast(cfg, quiet); err == nil {
		t.Error("default character without assets accepted")
	}
}
