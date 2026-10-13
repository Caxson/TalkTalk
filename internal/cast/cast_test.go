package cast

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func assetsWith(t *testing.T, files ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, f := range files {
		p := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

var sample = []Character{
	{ID: "mio", Name: "Mio", Kind: KindVRM, Model: "avatars/mio.vrm", Voice: "v1", Persona: "你叫 Mio。"},
	{ID: "ren", Name: "Ren", Kind: KindLAM, Model: "avatars/ren.zip", Voice: "v2"},
}

func TestOnlyCharactersWithAssetsAreAvailable(t *testing.T) {
	dir := assetsWith(t, "avatars/mio.vrm", "vendor/three-vrm.module.min.js", "vendor/three-vrm-animation.module.min.js", "vendor/three/three.module.js")
	c, err := New(sample, "", InDir(dir))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.List(); len(got) != 1 || got[0].ID != "mio" {
		t.Fatalf("List = %+v, want only mio (ren's files are missing)", got)
	}
	if c.Default().ID != "mio" {
		t.Errorf("Default = %q", c.Default().ID)
	}
	if _, ok := c.Get("ren"); ok {
		t.Error("Get(ren) succeeded without its assets")
	}
}

func TestRendererLibraryMustExistToo(t *testing.T) {
	dir := assetsWith(t, "avatars/mio.vrm") // model only, no three-vrm
	c, err := New(sample, "", InDir(dir))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.List()) != 0 {
		t.Errorf("List = %+v, want none without the VRM runtime", c.List())
	}
}

func TestDefaultChoice(t *testing.T) {
	dir := assetsWith(t, "avatars/mio.vrm", "avatars/ren.zip", "vendor/three-vrm.module.min.js",
		"vendor/three-vrm-animation.module.min.js", "vendor/three/three.module.js",
		"vendor/gaussian-splat-renderer-for-lam.module.js")
	c, err := New(sample, "ren", InDir(dir))
	if err != nil {
		t.Fatal(err)
	}
	if c.Default().ID != "ren" {
		t.Errorf("Default = %q, want ren", c.Default().ID)
	}
	if _, err := New(sample, "nobody", InDir(dir)); err == nil || !strings.Contains(err.Error(), "nobody") {
		t.Errorf("unknown default accepted: %v", err)
	}
}

func TestValidation(t *testing.T) {
	bad := [][]Character{
		{{ID: "", Name: "x", Kind: KindVRM, Model: "avatars/a.vrm", Voice: "v"}},
		{{ID: "a b", Name: "x", Kind: KindVRM, Model: "avatars/a.vrm", Voice: "v"}},
		{{ID: "a", Name: "", Kind: KindVRM, Model: "avatars/a.vrm", Voice: "v"}},
		{{ID: "a", Name: "x", Kind: "flash", Model: "avatars/a.vrm", Voice: "v"}},
		{{ID: "a", Name: "x", Kind: KindVRM, Model: "../etc/passwd", Voice: "v"}},
		{{ID: "a", Name: "x", Kind: KindVRM, Model: "a.vrm", Voice: "v"}},
		{{ID: "a", Name: "x", Kind: KindVRM}},
		{{ID: "a", Name: "x", Kind: KindVRM, Model: "avatars/a.vrm", Animation: "/abs.vrma"}},
		{{ID: "a", Name: "x", Kind: KindVRM, Model: "models/wav2arkit_cpu.onnx"}},
		{{ID: "a", Name: "x", Kind: KindVRM, Model: "avatars/../models/x.onnx"}},
		{{ID: "a", Name: "x", Kind: KindVRM, Model: `avatars\..\..\x`}},
		{{ID: "a", Name: "x", Kind: KindVRM, Model: "avatars//a.vrm"}},
		{{ID: "a", Name: "x", Kind: KindVRM, Model: "avatars/a.vrm", Voice: "v"}, {ID: "a", Name: "y", Kind: KindVRM, Model: "avatars/b.vrm", Voice: "v"}},
	}
	for i, cs := range bad {
		if _, err := New(cs, "", nil); err == nil {
			t.Errorf("case %d accepted: %+v", i, cs)
		}
	}
}

func TestAcceptsNamesThatMerelyStartWithDots(t *testing.T) {
	ok := []Character{{ID: "a", Name: "x", Kind: KindVRM, Model: "avatars/..odd/a.vrm"}}
	if _, err := New(ok, "", nil); err != nil {
		t.Error(err)
	}
}

func TestSystemPrompt(t *testing.T) {
	c := sample[0]
	if got := c.SystemPrompt("基础。"); got != "基础。\n你叫 Mio。" {
		t.Errorf("SystemPrompt = %q", got)
	}
	if got := sample[1].SystemPrompt("基础。"); got != "基础。" {
		t.Errorf("no persona: %q", got)
	}
}

func TestLoadFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cast.json")
	os.WriteFile(p, []byte(`[{"id":"kiki","name":"Kiki","kind":"vrm","model":"avatars/k.vrm","voice":"longanhuan","persona":"活泼。"}]`), 0o644)
	cs, err := LoadFile(p)
	if err != nil || len(cs) != 1 || cs[0].ID != "kiki" || cs[0].Voice != "longanhuan" {
		t.Fatalf("LoadFile = %+v, %v", cs, err)
	}
	os.WriteFile(p, []byte(`[{"id":"kiki","unknown":1}]`), 0o644)
	if _, err := LoadFile(p); err == nil {
		t.Error("unknown fields accepted")
	}
}

func TestBuiltinsAreValid(t *testing.T) {
	if _, err := New(Builtin(), "", nil); err != nil {
		t.Fatal(err)
	}
}

func TestVoiceOnlyKeepsEveryone(t *testing.T) {
	c, err := New(sample, "", nil)
	if err != nil || len(c.List()) != 2 {
		t.Fatalf("List = %+v, %v", c.List(), err)
	}
}
