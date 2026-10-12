package server

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func authedConfig(t *testing.T) Config {
	cfg := testConfig(t)
	cfg.Auth = NewAuth("deadbeef")
	// "example.com" is httptest.NewRequest's default Host; TestHostGuard
	// covers real rejection behavior with its own guard.
	cfg.Guard = NewHostGuard([]string{"example.com"})
	return cfg
}

func TestTokenRequired(t *testing.T) {
	h := New(authedConfig(t))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/state", nil))
	if rec.Code != 401 {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

func TestGETAcceptsHeaderOrQuery(t *testing.T) {
	h := New(authedConfig(t))
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/state", nil)
	r.Header.Set("X-Token", "deadbeef")
	h.ServeHTTP(rec, r)
	if rec.Code != 200 {
		t.Fatalf("header token: got %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/state?token=deadbeef", nil))
	if rec.Code != 200 {
		t.Fatalf("query token: got %d", rec.Code)
	}
}

func TestPOSTNeverAcceptsQueryToken(t *testing.T) { // Review Focus #5
	h := New(authedConfig(t))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/command?token=deadbeef", nil))
	if rec.Code != 401 {
		t.Fatalf("POST with query token only: want 401, got %d", rec.Code)
	}
}

func TestEmptyQueryTokenFails(t *testing.T) {
	h := New(authedConfig(t))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/state?token=", nil))
	if rec.Code != 401 {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

func TestHealthStaysTokenFree(t *testing.T) {
	h := New(authedConfig(t))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/health", nil))
	if rec.Code != 200 {
		t.Fatalf("health must not require a token: %d", rec.Code)
	}
}

func TestHostGuard(t *testing.T) {
	cfg := authedConfig(t)
	cfg.Guard = NewHostGuard([]string{"cliamp.home.lan"})
	h := New(cfg)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/state?token=deadbeef", nil)
	r.Host = "evil.example.com"
	h.ServeHTTP(rec, r)
	if rec.Code != 400 {
		t.Fatalf("want 400 on foreign Host, got %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	r2 := httptest.NewRequest("GET", "/api/state?token=deadbeef", nil)
	r2.Host = "cliamp.home.lan"
	h.ServeHTTP(rec, r2)
	if rec.Code != 200 {
		t.Fatalf("allowed host: got %d", rec.Code)
	}
}

func TestTokenFileCreated0600(t *testing.T) {
	dir := t.TempDir()
	tok, err := LoadOrCreateToken(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) != 64 {
		t.Fatalf("want 64 hex chars, got %d", len(tok))
	}
	b, err := os.ReadFile(filepath.Join(dir, "token"))
	if err != nil || strings.TrimSpace(string(b)) != tok {
		t.Fatalf("token file wrong: %q %v", b, err)
	}
	fi, _ := os.Stat(filepath.Join(dir, "token"))
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("want 0600, got %v", fi.Mode().Perm())
	}
	again, _ := LoadOrCreateToken(dir)
	if again != tok {
		t.Fatal("second load must return existing token")
	}
}

func TestStaticNeedsNoToken(t *testing.T) {
	cfg := authedConfig(t)
	h := New(cfg)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code == 401 {
		t.Fatal("static SPA must load without a token; the token guards data only")
	}
}
