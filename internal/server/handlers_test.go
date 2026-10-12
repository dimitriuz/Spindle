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
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["player"] != "offline" {
		t.Fatalf("want player=offline, got %q", body["player"])
	}
}
