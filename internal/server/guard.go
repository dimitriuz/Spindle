package server

import (
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// protect is the request guard for every data endpoint. There is no login:
// the bridge is meant for a trusted LAN. Two barriers remain against a
// hostile web page open in a browser on that LAN:
//   - the Host header must be a local address (DNS-rebinding), and
//   - state-changing requests must be JSON from the same origin. A cross-site
//     page cannot send application/json without a CORS preflight, which the
//     bridge never approves, and a foreign Origin header is refused outright.
func (s *serverState) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Guard != nil && !s.cfg.Guard.Allow(r) {
			reject(w, http.StatusBadRequest, "invalid_request")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
				reject(w, http.StatusUnsupportedMediaType, "invalid_request")
				return
			}
			if o := r.Header.Get("Origin"); o != "" {
				u, err := url.Parse(o)
				if err != nil || u.Host != r.Host {
					reject(w, http.StatusForbidden, "forbidden")
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
