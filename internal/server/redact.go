package server

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strings"
	"sync"
)

// Stream URLs from providers such as Navidrome carry the account's
// credentials in the query (u=, s=, t= …). The browser never needs them: it
// only echoes a track's path back to play or queue it. So every JSON body the
// bridge sends is scrubbed of those parameters, and the original URL is kept
// here so the two ops that take a track path get the real one back.

var secretParams = map[string]bool{
	"u": true, "s": true, "t": true, "p": true, // Subsonic / Navidrome
	"password": true, "pass": true, "token": true, "access_token": true,
	"api_key": true, "apikey": true, "x-plex-token": true, "signature": true,
}

const redactorCap = 20000

type Redactor struct {
	mu    sync.Mutex
	orig  map[string]string // scrubbed URL → original
	order []string          // insertion order, for FIFO eviction
}

func NewRedactor() *Redactor { return &Redactor{orig: map[string]string{}} }

// urls is the process-wide redactor: the bridge serves one player.
var urls = NewRedactor()

// scrubURL removes secret query parameters; ok=false means nothing changed.
func scrubURL(s string) (string, bool) {
	if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
		return s, false
	}
	u, err := url.Parse(s)
	if err != nil || u.RawQuery == "" {
		return s, false
	}
	q := u.Query()
	changed := false
	for k := range q {
		if secretParams[strings.ToLower(k)] {
			q.Del(k)
			changed = true
		}
	}
	if !changed {
		return s, false
	}
	u.RawQuery = q.Encode()
	return u.String(), true
}

func (r *Redactor) remember(scrubbed, original string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.orig[scrubbed]; !ok {
		if len(r.order) >= redactorCap {
			delete(r.orig, r.order[0])
			r.order = r.order[1:]
		}
		r.order = append(r.order, scrubbed)
	}
	r.orig[scrubbed] = original
}

// Restore maps a scrubbed URL back to the original; unknown strings pass
// through unchanged.
func (r *Redactor) Restore(s string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if o, ok := r.orig[s]; ok {
		return o
	}
	return s
}

func (r *Redactor) walk(v any) (any, bool) {
	switch x := v.(type) {
	case string:
		if n, ok := scrubURL(x); ok {
			r.remember(n, x)
			return n, true
		}
	case []any:
		changed := false
		for i := range x {
			if n, ok := r.walk(x[i]); ok {
				x[i], changed = n, true
			}
		}
		return x, changed
	case map[string]any:
		changed := false
		for k, e := range x {
			if n, ok := r.walk(e); ok {
				x[k], changed = n, true
			}
		}
		return x, changed
	}
	return v, false
}

// Scrub returns b with secret URL parameters removed; b itself when there is
// nothing to remove (the common case, decided by a cheap substring probe).
func (r *Redactor) Scrub(b []byte) []byte {
	if !bytes.Contains(b, []byte("http")) {
		return b
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return b
	}
	n, changed := r.walk(v)
	if !changed {
		return b
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if enc.Encode(n) != nil {
		return b
	}
	return bytes.TrimRight(out.Bytes(), "\n")
}
