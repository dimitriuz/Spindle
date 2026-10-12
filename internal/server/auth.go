package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// LoadOrCreateToken reads <dataDir>/token or creates a 256-bit one (0600).
func LoadOrCreateToken(dataDir string) (string, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dataDir, "token")
	if b, err := os.ReadFile(path); err == nil {
		return strings.TrimSpace(string(b)), nil
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(buf)
	if err := os.WriteFile(path, []byte(tok+"\n"), 0o600); err != nil {
		return "", err
	}
	return tok, nil
}

// Auth guards every /api/* endpoint except /api/health. POSTs require the
// X-Token header (cross-origin forms and plain fetches cannot set it, so a
// stolen browser session cannot issue commands); GETs accept the header or
// the ?token= query, which is how EventSource carries it.
type Auth struct{ token string }

func NewAuth(tok string) *Auth { return &Auth{token: tok} }

func (a *Auth) ok(got string) bool {
	return subtle.ConstantTimeCompare([]byte(a.token), []byte(got)) == 1
}

// protect is the Host-guard + token middleware for data endpoints. A nil
// Auth/Guard disables the respective check (tests and health).
func (s *serverState) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Guard != nil && !s.cfg.Guard.Allow(r) {
			reject(w, http.StatusBadRequest, "invalid_request")
			return
		}
		if s.cfg.Auth != nil {
			hdr := r.Header.Get("X-Token")
			if r.Method == http.MethodPost {
				if !s.cfg.Auth.ok(hdr) {
					reject(w, http.StatusUnauthorized, "unauthorized")
					return
				}
			} else {
				q := r.URL.Query().Get("token")
				if hdr == "" && q == "" {
					reject(w, http.StatusUnauthorized, "unauthorized")
					return
				}
				if !s.cfg.Auth.ok(hdr) && !s.cfg.Auth.ok(q) {
					reject(w, http.StatusUnauthorized, "unauthorized")
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func reject(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"code":"` + code + `"}`))
}

// HostGuard rejects requests whose Host header is neither a local interface
// address, localhost, nor a configured extra — the DNS-rebinding barrier.
type HostGuard struct {
	allowed map[string]bool
}

func NewHostGuard(extra []string) *HostGuard {
	g := &HostGuard{allowed: map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true}}
	for _, e := range extra {
		if e = strings.TrimSpace(e); e != "" {
			g.allowed[e] = true
		}
	}
	if ifas, err := net.InterfaceAddrs(); err == nil {
		for _, a := range ifas {
			if ipn, ok := a.(*net.IPNet); ok {
				g.allowed[ipn.IP.String()] = true
			}
		}
	}
	return g
}

func (g *HostGuard) Allow(r *http.Request) bool {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return g.allowed[host]
}
