package art

import (
	"bufio"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// NavidromeConfig is the login of the user's own Navidrome server, as cliamp
// has it in its config. It signs the bridge's cover requests and goes nowhere
// else: never logged, never sent to the browser.
type NavidromeConfig struct{ URL, User, Password string }

// NavidromeFromConfig reads the [navidrome] section of cliamp's config.toml.
func NavidromeFromConfig(configDir string) (NavidromeConfig, bool) {
	f, err := os.Open(filepath.Join(configDir, "cliamp", "config.toml"))
	if err != nil {
		return NavidromeConfig{}, false
	}
	defer f.Close()
	var c NavidromeConfig
	in := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			in = strings.TrimSpace(strings.Trim(line, "[]")) == "navidrome"
			continue
		}
		if !in {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "url":
			c.URL = tomlString(val)
		case "user":
			c.User = tomlString(val)
		case "password":
			c.Password = tomlString(val)
		}
	}
	c.URL = strings.TrimRight(c.URL, "/")
	if c.URL == "" || c.User == "" || c.Password == "" || !(strings.HasPrefix(c.URL, "http://") || strings.HasPrefix(c.URL, "https://")) {
		return NavidromeConfig{}, false
	}
	return c, true
}

// tomlString reads a quoted or bare TOML value, dropping a trailing comment.
func tomlString(val string) string {
	val = strings.TrimSpace(val)
	if val != "" && (val[0] == '"' || val[0] == '\'') {
		if end := strings.IndexByte(val[1:], val[0]); end >= 0 {
			return val[1 : 1+end]
		}
		return val[1:]
	}
	if i := strings.IndexByte(val, '#'); i >= 0 {
		val = val[:i]
	}
	return strings.TrimSpace(val)
}

// query signs a Subsonic request with token auth: t = md5(password + salt),
// a fresh random salt each time; the password itself is never sent.
func (c NavidromeConfig) query(extra url.Values) url.Values {
	salt := make([]byte, 8)
	_, _ = rand.Read(salt)
	s := hex.EncodeToString(salt)
	sum := md5.Sum([]byte(c.Password + s))
	q := url.Values{"u": {c.User}, "s": {s}, "t": {hex.EncodeToString(sum[:])}, "v": {"1.16.1"}, "c": {"spindle"}}
	for k, v := range extra {
		q[k] = v
	}
	return q
}

var itemIDRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,80}$`)

// ValidItemID says whether id is safe to put in a Subsonic request.
func ValidItemID(id string) bool { return itemIDRe.MatchString(id) && !strings.Contains(id, "..") }
