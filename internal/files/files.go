// Package files lists and walks the music folder cliamp is set up with, so the
// web page can offer the same file browser as the TUI. cliamp has no operation
// for listing directories; the bridge runs on the same machine and does it
// itself, confined to one root.
package files

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

var (
	// ErrOutside: the path leaves the music root (dot-dot, an absolute path, or a symlink out).
	ErrOutside = errors.New("files: path is outside the music folder")
	// ErrNotFound: nothing there, unreadable, or not something that can be played.
	ErrNotFound = errors.New("files: not found")
)

const maxDepth = 16

var audioExt = map[string]bool{
	".flac": true, ".mp3": true, ".m4a": true, ".aac": true, ".ogg": true, ".opus": true, ".wav": true,
	".wma": true, ".aiff": true, ".aif": true, ".ape": true, ".wv": true, ".mka": true, ".alac": true,
}

func isAudio(name string) bool { return audioExt[strings.ToLower(filepath.Ext(name))] }

type Entry struct {
	Name string `json:"name"`
	Dir  bool   `json:"dir"`
}

// Listing is one folder as the page shows it. Path and Parent are relative to
// the root, with "/" separators; nothing here is an absolute path.
type Listing struct {
	Path    string  `json:"path"`
	Parent  string  `json:"parent"`
	Name    string  `json:"name"`
	Entries []Entry `json:"entries"`
}

type Browser struct {
	root string // resolved: no symlinks left in it
	name string
}

// New opens root, which must be a directory.
func New(root string) (*Browser, error) {
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(real)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("files: %s is not a directory", filepath.Base(root))
	}
	return &Browser{root: real, name: filepath.Base(real)}, nil
}

func (b *Browser) RootName() string { return b.name }

func (b *Browser) inside(real string) bool {
	return real == b.root || strings.HasPrefix(real, b.root+string(filepath.Separator))
}

// clean validates a client-supplied relative path.
func clean(rel string) (string, error) {
	if strings.ContainsAny(rel, "\x00\\") || strings.HasPrefix(rel, "/") {
		return "", ErrOutside
	}
	for _, part := range strings.Split(rel, "/") {
		if part == ".." {
			return "", ErrOutside
		}
	}
	c := path.Clean(rel)
	if c == "." {
		return "", nil
	}
	return c, nil
}

// resolve returns the real (symlink-free) location of rel inside the root.
func (b *Browser) resolve(rel string) (cleaned, real string, err error) {
	cleaned, err = clean(rel)
	if err != nil {
		return "", "", err
	}
	real, err = filepath.EvalSymlinks(filepath.Join(b.root, filepath.FromSlash(cleaned)))
	if err != nil {
		return "", "", ErrNotFound
	}
	if !b.inside(real) {
		return "", "", ErrOutside
	}
	return cleaned, real, nil
}

// Resolve gives the absolute path of a playable file or a folder.
func (b *Browser) Resolve(rel string) (abs string, dir bool, err error) {
	_, real, err := b.resolve(rel)
	if err != nil {
		return "", false, err
	}
	fi, err := os.Stat(real)
	if err != nil {
		return "", false, ErrNotFound
	}
	if !fi.IsDir() && !isAudio(real) {
		return "", false, ErrNotFound
	}
	return real, fi.IsDir(), nil
}

// kind classifies a directory entry, following symlinks that stay inside the root.
func (b *Browser) kind(dir string, de fs.DirEntry) (isDir, ok bool) {
	full := filepath.Join(dir, de.Name())
	if de.Type()&fs.ModeSymlink != 0 {
		real, err := filepath.EvalSymlinks(full)
		if err != nil || !b.inside(real) {
			return false, false
		}
		fi, err := os.Stat(real)
		if err != nil {
			return false, false
		}
		return fi.IsDir(), fi.IsDir() || isAudio(real)
	}
	if de.IsDir() {
		return true, true
	}
	return false, isAudio(de.Name())
}

// List shows one folder: sub-folders first, then audio files, in natural order.
func (b *Browser) List(rel string) (Listing, error) {
	cleaned, real, err := b.resolve(rel)
	if err != nil {
		return Listing{}, err
	}
	des, err := os.ReadDir(real)
	if err != nil {
		return Listing{}, ErrNotFound
	}
	var dirs, fl []Entry
	for _, de := range des {
		if strings.HasPrefix(de.Name(), ".") {
			continue
		}
		isDir, ok := b.kind(real, de)
		if !ok {
			continue
		}
		if isDir {
			dirs = append(dirs, Entry{Name: de.Name(), Dir: true})
		} else {
			fl = append(fl, Entry{Name: de.Name()})
		}
	}
	less := func(s []Entry) func(i, j int) bool {
		return func(i, j int) bool { return NaturalLess(s[i].Name, s[j].Name) }
	}
	sort.Slice(dirs, less(dirs))
	sort.Slice(fl, less(fl))
	l := Listing{Path: cleaned, Entries: append(dirs, fl...), Name: b.name}
	if cleaned != "" {
		l.Name = path.Base(cleaned)
		if p := path.Dir(cleaned); p != "." {
			l.Parent = p
		}
	}
	if l.Entries == nil {
		l.Entries = []Entry{}
	}
	return l, nil
}

// Walk lists every audio file under rel (or rel itself when it is a file),
// recursively, in natural order. Unreadable folders are skipped.
func (b *Browser) Walk(ctx context.Context, rel string) ([]string, error) {
	_, real, err := b.resolve(rel)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(real)
	if err != nil {
		return nil, ErrNotFound
	}
	if !fi.IsDir() {
		if !isAudio(real) {
			return nil, ErrNotFound
		}
		return []string{real}, nil
	}
	var out []string
	visited := map[string]bool{}
	var walk func(dir string, depth int) error
	walk = func(dir string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if depth > maxDepth || visited[dir] {
			return nil
		}
		visited[dir] = true
		des, err := os.ReadDir(dir)
		if err != nil {
			return nil
		}
		sort.Slice(des, func(i, j int) bool { return NaturalLess(des[i].Name(), des[j].Name()) })
		for _, de := range des {
			if strings.HasPrefix(de.Name(), ".") {
				continue
			}
			isDir, ok := b.kind(dir, de)
			if !ok {
				continue
			}
			real, err := filepath.EvalSymlinks(filepath.Join(dir, de.Name()))
			if err != nil || !b.inside(real) {
				continue
			}
			if isDir {
				if err := walk(real, depth+1); err != nil {
					return err
				}
			} else {
				out = append(out, real)
			}
		}
		return nil
	}
	if err := walk(real, 0); err != nil {
		return nil, err
	}
	return out, nil
}

var coverNames = []string{"cover", "folder", "front", "album"}
var coverExts = []string{".jpg", ".jpeg", ".png"}

// findCover looks for cover|folder|front|album .jpg|.jpeg|.png in one folder
// (case-insensitive), ignoring pictures that leave the root or are too big.
func (b *Browser) findCover(dir string) (string, bool) {
	des, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	have := map[string]string{}
	for _, de := range des {
		have[strings.ToLower(de.Name())] = de.Name()
	}
	for _, n := range coverNames {
		for _, e := range coverExts {
			name, ok := have[n+e]
			if !ok {
				continue
			}
			real, err := filepath.EvalSymlinks(filepath.Join(dir, name))
			if err != nil || !b.inside(real) {
				continue
			}
			if fi, err := os.Stat(real); err != nil || !fi.Mode().IsRegular() || fi.Size() > 10<<20 {
				continue
			}
			return real, true
		}
	}
	return "", false
}

// CoverFile finds a folder's cover picture: in the folder itself, else in its
// first sub-folder (so an artist folder shows its first album). ErrNotFound if none.
func (b *Browser) CoverFile(rel string) (string, error) {
	_, real, err := b.resolve(rel)
	if err != nil {
		return "", err
	}
	if fi, err := os.Stat(real); err != nil || !fi.IsDir() {
		return "", ErrNotFound
	}
	if p, ok := b.findCover(real); ok {
		return p, nil
	}
	des, err := os.ReadDir(real)
	if err != nil {
		return "", ErrNotFound
	}
	sort.Slice(des, func(i, j int) bool { return NaturalLess(des[i].Name(), des[j].Name()) })
	for _, de := range des {
		if strings.HasPrefix(de.Name(), ".") {
			continue
		}
		if isDir, ok := b.kind(real, de); ok && isDir {
			sub, err := filepath.EvalSymlinks(filepath.Join(real, de.Name()))
			if err != nil || !b.inside(sub) {
				continue
			}
			if p, ok := b.findCover(sub); ok {
				return p, nil
			}
			return "", ErrNotFound // only the first sub-folder is looked at
		}
	}
	return "", ErrNotFound
}

// RootFromConfig is cliamp's initial_directory (its file browser's start), with
// ~ expanded, or ~/Music when the config does not say.
func RootFromConfig(configDir, home string) string {
	def := filepath.Join(home, "Music")
	f, err := os.Open(filepath.Join(configDir, "cliamp", "config.toml"))
	if err != nil {
		return def
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			break // only the top-level keys count
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != "initial_directory" {
			continue
		}
		val = strings.TrimSpace(val)
		if val != "" && (val[0] == '"' || val[0] == '\'') {
			if end := strings.IndexByte(val[1:], val[0]); end >= 0 {
				val = val[1 : 1+end]
			}
		} else if i := strings.IndexByte(val, '#'); i >= 0 {
			val = strings.TrimSpace(val[:i])
		}
		switch {
		case val == "":
			return def
		case val == "~":
			return filepath.Clean(home)
		case strings.HasPrefix(val, "~/"):
			return filepath.Clean(filepath.Join(home, val[2:]))
		}
		return filepath.Clean(val)
	}
	return def
}

// NaturalLess orders names the way a person expects: case-insensitive, and
// digit runs compare as numbers ("Track 2" before "Track 10"; "01" before "1").
func NaturalLess(a, b string) bool {
	ra, rb := []rune(strings.ToLower(a)), []rune(strings.ToLower(b))
	i, j := 0, 0
	for i < len(ra) && j < len(rb) {
		if unicode.IsDigit(ra[i]) && unicode.IsDigit(rb[j]) {
			si, sj := i, j
			for i < len(ra) && unicode.IsDigit(ra[i]) {
				i++
			}
			for j < len(rb) && unicode.IsDigit(rb[j]) {
				j++
			}
			na, nb := strings.TrimLeft(string(ra[si:i]), "0"), strings.TrimLeft(string(rb[sj:j]), "0")
			if len(na) != len(nb) {
				return len(na) < len(nb)
			}
			if na != nb {
				return na < nb
			}
			if la, lb := i-si, j-sj; la != lb {
				return la > lb // equal numbers: more leading zeros first
			}
			continue
		}
		if ra[i] != rb[j] {
			return ra[i] < rb[j]
		}
		i++
		j++
	}
	return len(ra)-i < len(rb)-j
}
