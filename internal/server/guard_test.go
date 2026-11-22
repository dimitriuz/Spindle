package server

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func guardedConfig(t *testing.T) Config {
	cfg := testConfig(t)
	// "example.com" is httptest.NewRequest's default Host; TestHostGuard
	// covers real rejection behavior with its own guard.
	cfg.Guard = NewHostGuard([]string{"example.com"})
	return cfg
}

func TestNoTokenNeeded(t *testing.T) {
	h := New(guardedConfig(t))
	for _, path := range []string{"/api/state", "/api/queue", "/api/capabilities"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 {
			t.Fatalf("%s: want 200 without any credential, got %d", path, rec.Code)
		}
	}
}

// With no secret to hold, the CSRF barrier is: JSON only, same origin only.
func TestPOSTMustBeJSON(t *testing.T) {
	cfg := guardedConfig(t)
	cfg.Runner = &fakeRunner{}
	h := New(cfg)
	for _, ct := range []string{"", "text/plain", "application/x-www-form-urlencoded", "multipart/form-data"} {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/command", strings.NewReader(`{"op":"toggle"}`))
		if ct != "" {
			r.Header.Set("Content-Type", ct)
		}
		h.ServeHTTP(rec, r)
		if rec.Code != 415 {
			t.Fatalf("content-type %q: want 415, got %d", ct, rec.Code)
		}
	}
}

func TestPOSTRejectsForeignOrigin(t *testing.T) {
	cfg := guardedConfig(t)
	cfg.Runner = &fakeRunner{}
	h := New(cfg)
	post := func(origin string) int {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/command", strings.NewReader(`{"op":"toggle"}`))
		r.Header.Set("Content-Type", "application/json")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		h.ServeHTTP(rec, r)
		return rec.Code
	}
	if c := post("http://evil.example.org"); c != 403 {
		t.Fatalf("foreign origin: want 403, got %d", c)
	}
	if c := post("http://example.com"); c != 200 {
		t.Fatalf("same origin: want 200, got %d", c)
	}
	if c := post(""); c != 200 {
		t.Fatalf("no Origin (curl, same-origin fetch): want 200, got %d", c)
	}
}

func TestHealthIsOpen(t *testing.T) {
	h := New(guardedConfig(t))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/health", nil))
	if rec.Code != 200 {
		t.Fatalf("health: %d", rec.Code)
	}
}
