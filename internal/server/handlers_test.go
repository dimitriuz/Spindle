package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"

	"cliamp-web/internal/snapshot"
)

func testConfig(t *testing.T) Config {
	return Config{Cache: snapshot.NewCache(), Hub: NewHub(),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func TestHealthReportsOffline(t *testing.T) {
	h := New(testConfig(t))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/health", nil))
	if rec.Code != 200 {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["player"] != "offline" {
		t.Fatalf("want player=offline, got %q", body["player"])
	}
	if body["ok"] == nil {
		t.Fatalf("health must report ok, got %v", body)
	}
}

func TestHealthReportsOnline(t *testing.T) {
	cfg := testConfig(t)
	cfg.Cache.SetOnline(true)
	h := New(cfg)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/health", nil))
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["player"] != "online" {
		t.Fatalf("want player=online, got %q", body["player"])
	}
}
