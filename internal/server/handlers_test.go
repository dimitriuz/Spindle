package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cliamp-web/internal/ipc"
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

type fakeRunner struct {
	lastOp     string
	lastParams map[string]any
	job        *ipc.Job
	err        error
}

func (f *fakeRunner) Call(_ context.Context, _ string, _ any) (*ipc.Response, error) {
	return &ipc.Response{OK: true}, nil
}

func (f *fakeRunner) SubmitJob(_ context.Context, op string, p any) (*ipc.Job, error) {
	f.lastOp = op
	f.lastParams, _ = p.(map[string]any)
	if f.err != nil {
		return f.job, f.err
	}
	return f.job, nil
}

func commandConfig(t *testing.T, fr *fakeRunner) Config {
	cfg := authedConfig(t)
	cfg.Runner = fr
	cfg.Limiter = NewLimiter(20, 40, time.Now)
	return cfg
}

func postCommand(h http.Handler, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/command", strings.NewReader(body))
	r.Header.Set("X-Token", "deadbeef")
	h.ServeHTTP(rec, r)
	return rec
}

func TestCommandSuccessReturnsJob(t *testing.T) {
	fr := &fakeRunner{job: &ipc.Job{ID: "j", State: "succeeded", Snapshot: json.RawMessage(`{"revision":9}`)}}
	h := New(commandConfig(t, fr))
	rec := postCommand(h, `{"op":"queue.play","params":{"index":4,"if_revision":7}}`)
	if rec.Code != 200 {
		t.Fatalf("got %d body %s", rec.Code, rec.Body)
	}
	if fr.lastOp != "queue.play" || fr.lastParams["if_revision"] != float64(7) {
		t.Fatalf("params passthrough broken: %+v", fr.lastParams)
	}
	if !strings.Contains(rec.Body.String(), `"succeeded"`) {
		t.Fatalf("job not echoed: %s", rec.Body)
	}
}

func TestCommandConflictMapsTo409(t *testing.T) { // Review Focus #4
	fr := &fakeRunner{job: &ipc.Job{State: "failed"}, err: &ipc.IPCError{Code: "conflict", Detail: "stale"}}
	h := New(commandConfig(t, fr))
	rec := postCommand(h, `{"op":"queue.move","params":{"index":1,"to":0,"if_revision":3}}`)
	if rec.Code != 409 {
		t.Fatalf("want 409, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "conflict") {
		t.Fatalf("body must name code: %s", rec.Body)
	}
}

func TestCommandUnavailableMapsTo503(t *testing.T) {
	fr := &fakeRunner{err: ipc.ErrNotConnected}
	h := New(commandConfig(t, fr))
	rec := postCommand(h, `{"op":"toggle"}`)
	if rec.Code != 503 {
		t.Fatalf("want 503, got %d", rec.Code)
	}
}

func TestCommandInvalidBodyMapsTo400(t *testing.T) {
	fr := &fakeRunner{job: &ipc.Job{State: "succeeded"}}
	h := New(commandConfig(t, fr))
	rec := postCommand(h, `{"params":{}}`)
	if rec.Code != 400 {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestRateLimitRefill(t *testing.T) {
	now := time.Now
	l := NewLimiter(2, 3, func() time.Time { return now() })
	for range 3 {
		if !l.Allow("1.2.3.4") {
			t.Fatal("burst should allow 3")
		}
	}
	if l.Allow("1.2.3.4") {
		t.Fatal("4th immediate must be rejected")
	}
	base := now()
	now = func() time.Time { return base.Add(time.Second) }
	if !l.Allow("1.2.3.4") {
		t.Fatal("after 1s refill want allow")
	}
}

func TestRateLimitEndpoint429(t *testing.T) {
	fr := &fakeRunner{job: &ipc.Job{State: "succeeded"}}
	cfg := commandConfig(t, fr)
	cfg.Limiter = NewLimiter(0, 2, time.Now)
	h := New(cfg)
	for range 2 {
		if rec := postCommand(h, `{"op":"toggle"}`); rec.Code != 200 {
			t.Fatalf("within burst: %d", rec.Code)
		}
	}
	if rec := postCommand(h, `{"op":"toggle"}`); rec.Code != 429 {
		t.Fatalf("want 429, got %d", rec.Code)
	}
}

func TestQueueGETServesCache(t *testing.T) {
	cfg := authedConfig(t)
	cfg.Cache.AbsorbQueueResult(json.RawMessage(`{"tracks":[{"path":"a"}]}`), 5)
	h := New(cfg)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/queue", nil)
	r.Header.Set("X-Token", "deadbeef")
	h.ServeHTTP(rec, r)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"playlist_revision":5`) {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestCapabilitiesGETServesCache(t *testing.T) {
	cfg := authedConfig(t)
	cfg.Cache.SetCaps(json.RawMessage(`{"operations":["play"]}`))
	h := New(cfg)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/capabilities", nil)
	r.Header.Set("X-Token", "deadbeef")
	h.ServeHTTP(rec, r)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"play"`) {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}
