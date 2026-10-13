package server

import (
	"errors"
	"io/fs"
	"path"
	"strings"
)

// PublicFS exposes only regular files under the given top-level
// directories of fsys: no directory listings, nothing else (the lip-sync
// model next to them must not be downloadable).
func PublicFS(fsys fs.FS, dirs ...string) fs.FS {
	return publicFS{fsys: fsys, dirs: dirs}
}

type publicFS struct {
	fsys fs.FS
	dirs []string
}

func (p publicFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) || !p.allowed(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	f, err := p.fsys.Open(name)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		f.Close()
		return nil, &fs.PathError{Op: "open", Path: name, Err: errors.Join(fs.ErrNotExist, err)}
	}
	return f, nil
}

func (p publicFS) allowed(name string) bool {
	clean := path.Clean(name)
	for _, d := range p.dirs {
		if strings.HasPrefix(clean, d+"/") {
			return true
		}
	}
	return false
}
