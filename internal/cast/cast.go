// Package cast is the roster of characters the assistant can be: each has
// a face (an avatar for one of the browser renderers), a voice and a
// persona. The motion that animates every face is the same ARKit stream;
// only how it is drawn differs.
package cast

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Renderer kinds.
const (
	KindVRM    = "vrm"    // anime 3D (three-vrm)
	KindLAM    = "lam"    // photoreal Gaussian splats
	KindLive2D = "live2d" // anime 2D
	// KindVideo is photoreal video rendered by the GPU worker; Model names
	// the worker's character and Background is the still shown before
	// video arrives.
	KindVideo = "video"
)

// runtime lists the files each renderer needs, relative to the assets dir.
var runtime = map[string][]string{
	KindVRM:    {"vendor/three-vrm.module.min.js", "vendor/three-vrm-animation.module.min.js", "vendor/three/three.module.js"},
	KindLAM:    {"vendor/gaussian-splat-renderer-for-lam.module.js"},
	KindLive2D: {"vendor/live2d/live2dcubismcore.min.js", "vendor/live2d/cubism.es.js", "vendor/pixi/pixi.min.mjs"},
	KindVideo:  {},
}

// RendererModule is the ES module each renderer's client code loads first.
var RendererModule = map[string]string{
	KindVRM:    "vendor/three-vrm.module.min.js",
	KindLAM:    "vendor/gaussian-splat-renderer-for-lam.module.js",
	KindLive2D: "vendor/live2d/cubism.es.js", // needs Cubism Core beside it
}

// Character is one member of the cast.
type Character struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Tagline   string `json:"tagline,omitempty"`
	Kind      string `json:"kind"`
	Model     string `json:"model"`               // avatar file, relative to the assets dir
	Animation string `json:"animation,omitempty"` // idle motion (VRM)
	// Background, for a photoreal head (LAM), is the photo it was made
	// from: it supplies hair, body and room, and the head is fitted onto it.
	Background string `json:"background,omitempty"`
	Voice      string `json:"voice,omitempty"`   // TTS voice id ("" = the configured default)
	Persona    string `json:"persona,omitempty"` // appended to the system prompt
	// Credit is shown with the avatar when its license asks for one.
	Credit string `json:"credit,omitempty"`
}

// SystemPrompt combines the base prompt with the character's persona.
func (c Character) SystemPrompt(base string) string {
	if c.Persona == "" {
		return base
	}
	return base + "\n" + c.Persona
}

var validID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

func (c Character) validate() error {
	switch {
	case !validID.MatchString(c.ID):
		return fmt.Errorf("id %q: want lowercase letters, digits, - or _", c.ID)
	case c.Name == "":
		return fmt.Errorf("%s: name is required", c.ID)
	case runtime[c.Kind] == nil:
		return fmt.Errorf("%s: unknown kind %q", c.ID, c.Kind)
	}
	if c.Model == "" {
		return fmt.Errorf("%s: model is required", c.ID)
	}
	if c.Kind == KindVideo && !validID.MatchString(c.Model) {
		return fmt.Errorf("%s: model %q must be the worker's character id", c.ID, c.Model)
	}
	for _, p := range c.files() {
		if p != "" && !isLocalPath(p) {
			return fmt.Errorf("%s: asset path %q must be a clean relative path under avatars/ or vendor/", c.ID, p)
		}
	}
	return nil
}

// files are the character's own asset files (a video character's model
// lives on the worker, not here).
func (c Character) files() []string {
	model := c.Model
	if c.Kind == KindVideo {
		model = ""
	}
	return []string{model, c.Animation, c.Background}
}

// servedDirs are the asset folders the server publishes (main.go).
var servedDirs = []string{"avatars/", "vendor/"}

// isLocalPath accepts clean slash paths inside a served asset folder, so a
// character can never point the existence check or the browser elsewhere.
func isLocalPath(p string) bool {
	if !fs.ValidPath(p) || strings.Contains(p, `\`) {
		return false
	}
	for _, d := range servedDirs {
		if strings.HasPrefix(p, d) {
			return true
		}
	}
	return false
}

// Cast is the validated roster, limited to characters whose files exist.
type Cast struct {
	chars []Character
	def   Character
}

// Exists reports whether an asset (a path relative to the assets dir) is
// present.
type Exists func(path string) bool

// InDir checks for assets under dir.
func InDir(dir string) Exists {
	return func(p string) bool {
		_, err := os.Stat(filepath.Join(dir, filepath.FromSlash(p)))
		return err == nil
	}
}

// New validates chars and keeps those whose avatar and renderer files
// exist (nil exists: keep all, for voice-only conversations). def picks
// the default (empty: the first available).
func New(chars []Character, def string, exists Exists) (*Cast, error) {
	seen := map[string]bool{}
	for _, c := range chars {
		if err := c.validate(); err != nil {
			return nil, fmt.Errorf("cast: %w", err)
		}
		if seen[c.ID] {
			return nil, fmt.Errorf("cast: duplicate id %q", c.ID)
		}
		seen[c.ID] = true
	}
	if def != "" && !seen[def] {
		return nil, fmt.Errorf("cast: default character %q is not in the cast", def)
	}
	cs := &Cast{}
	for _, c := range chars {
		if exists == nil || present(exists, append(c.files(), runtime[c.Kind]...)) {
			cs.chars = append(cs.chars, c)
		}
	}
	for _, c := range cs.chars {
		if def == "" || c.ID == def {
			cs.def = c
			break
		}
	}
	return cs, nil
}

func present(exists Exists, files []string) bool {
	for _, f := range files {
		if f != "" && !exists(f) {
			return false
		}
	}
	return true
}

// List returns the available characters in roster order.
func (c *Cast) List() []Character { return append([]Character(nil), c.chars...) }

// Default is the character used when a session names none. Its zero value
// (empty ID) means no character is available.
func (c *Cast) Default() Character { return c.def }

// Get finds an available character.
func (c *Cast) Get(id string) (Character, bool) {
	for _, ch := range c.chars {
		if ch.ID == id {
			return ch, true
		}
	}
	return Character{}, false
}

// LoadFile reads a roster from a JSON array, rejecting unknown fields.
func LoadFile(p string) ([]Character, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, fmt.Errorf("cast: %w", err)
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	var cs []Character
	if err := dec.Decode(&cs); err != nil {
		return nil, fmt.Errorf("cast: %s: %w", p, err)
	}
	if len(cs) == 0 {
		return nil, errors.New("cast: " + p + " defines no characters")
	}
	return cs, nil
}
