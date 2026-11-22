package art

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/dhowden/tag"
)

var (
	imageExts  = []string{".jpg", ".jpeg", ".png", ".webp"}
	coverNames = []string{"cover", "folder", "front", "album"}
)

// Local finds art next to a track on this machine. It only ever opens the
// track itself (for an embedded picture) and image files inside the track's
// own album directory, or its parent for an artist photo.
type Local struct {
	// Known reports whether path is a track the bridge has been told about.
	// Anything else is refused, so the route can never be used to read files.
	Known func(path string) bool
}

// Resolve returns a key that changes whenever the picture changes, and a
// function that reads it. ErrNoArt when there is none or the path is refused.
func (l *Local) Resolve(kind Kind, trackPath string) (string, func() ([]byte, error), error) {
	if trackPath == "" || !filepath.IsAbs(trackPath) || filepath.Clean(trackPath) != trackPath ||
		l.Known == nil || !l.Known(trackPath) {
		return "", nil, ErrNoArt
	}
	if fi, err := os.Stat(trackPath); err != nil || !fi.Mode().IsRegular() {
		return "", nil, ErrNoArt
	}
	albumDir := filepath.Dir(trackPath)

	var file string // a picture file on disk, or "" for the embedded one
	switch kind {
	case Cover:
		if embedded(trackPath) != nil {
			return localKey(kind, trackPath), func() ([]byte, error) { return readEmbedded(trackPath) }, nil
		}
		file = findImage(albumDir, albumDir, coverNames)
	case Disc:
		file = findNamed(albumDir, albumDir, []string{"disc.png", "cdart.png"})
	case Artist:
		file = findImage(albumDir, albumDir, []string{"artist"})
		if file == "" {
			parent := filepath.Dir(albumDir)
			file = findImage(parent, parent, []string{"artist"})
		}
	default:
		return "", nil, ErrNoArt
	}
	if file == "" {
		return "", nil, ErrNoArt
	}
	return localKey(kind, file), func() ([]byte, error) { return readImage(file) }, nil
}

// localKey ties the key to the file's identity and mtime, so replacing a
// cover.png shows up without clearing the cache.
func localKey(kind Kind, file string) string {
	var stamp string
	if fi, err := os.Stat(file); err == nil {
		stamp = fmt.Sprintf("%d:%d", fi.Size(), fi.ModTime().UnixNano())
	}
	h := sha256.Sum256([]byte(file + "|" + stamp))
	return "local:" + string(kind) + ":" + hex.EncodeToString(h[:8])
}

func embedded(trackPath string) *tag.Picture {
	f, err := os.Open(trackPath)
	if err != nil {
		return nil
	}
	defer f.Close()
	m, err := tag.ReadFrom(f)
	if err != nil {
		return nil
	}
	p := m.Picture()
	if p == nil || len(p.Data) == 0 || len(p.Data) > MaxImageBytes {
		return nil
	}
	return p
}

func readEmbedded(trackPath string) ([]byte, error) {
	p := embedded(trackPath)
	if p == nil {
		return nil, ErrNoArt
	}
	return p.Data, nil
}

// findImage returns the first file in dir called <name><image extension>,
// names in priority order, matched case-insensitively and kept inside root.
func findImage(dir, root string, names []string) string {
	var cands []string
	for _, n := range names {
		for _, e := range imageExts {
			cands = append(cands, n+e)
		}
	}
	return findNamed(dir, root, cands)
}

func findNamed(dir, root string, cands []string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	have := map[string]string{}
	for _, e := range entries {
		have[strings.ToLower(e.Name())] = e.Name()
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return ""
	}
	for _, c := range cands {
		name, ok := have[c]
		if !ok {
			continue
		}
		real, err := filepath.EvalSymlinks(filepath.Join(dir, name))
		if err != nil || !strings.HasPrefix(real, realRoot+string(filepath.Separator)) {
			continue // a link leading out of the directory is not ours to read
		}
		if fi, err := os.Stat(real); err != nil || !fi.Mode().IsRegular() || fi.Size() > MaxImageBytes {
			continue
		}
		return real
	}
	return ""
}

func readImage(file string) ([]byte, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, ErrNoArt
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxImageBytes+1))
	if err != nil || len(data) > MaxImageBytes || !strings.HasPrefix(http.DetectContentType(data), "image/") {
		return nil, ErrNoArt
	}
	return data, nil
}
