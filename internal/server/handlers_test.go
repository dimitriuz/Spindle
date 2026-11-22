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
	errFn      func(ctx context.Context) error
	okFn       func()
	calls      int
}

func (f *fakeRunner) Call(_ context.Context, _ string, _ any) (*ipc.Response, error) {
	return &ipc.Response{OK: true}, nil
}

func (f *fakeRunner) SubmitJob(ctx context.Context, op string, p any) (*ipc.Job, error) {
	f.calls++
	f.lastOp = op
	f.lastParams, _ = p.(map[string]any)
	if f.errFn != nil {
		return nil, f.errFn(ctx)
	}
	if f.err != nil {
		return f.job, f.err
	}
	if f.okFn != nil {
		f.okFn()
	}
	return f.job, nil
}

func commandConfig(t *testing.T, fr *fakeRunner) Config {
	cfg := guardedConfig(t)
	cfg.Runner = fr
	cfg.Limiter = NewLimiter(20, 40, time.Now)
	return cfg
}

func postCommand(h http.Handler, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/command", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
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
	cfg := guardedConfig(t)
	cfg.Cache.AbsorbQueueResult(json.RawMessage(`{"tracks":[{"path":"a"}]}`), 5)
	h := New(cfg)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/queue", nil)
	h.ServeHTTP(rec, r)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"playlist_revision":5`) {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestCapabilitiesGETServesCache(t *testing.T) {
	cfg := guardedConfig(t)
	cfg.Cache.SetCaps(json.RawMessage(`{"operations":["play"]}`))
	h := New(cfg)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/capabilities", nil)
	h.ServeHTTP(rec, r)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"play"`) {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestCommandRefusesOpsTheUIDoesNotUse(t *testing.T) {
	fr := &fakeRunner{job: &ipc.Job{State: "succeeded"}}
	h := New(commandConfig(t, fr))
	for _, op := range []string{"plugin.call", "url.load", "history.clear", "playlist.delete", "system.exec"} {
		rec := postCommand(h, `{"op":"`+op+`"}`)
		if rec.Code != 403 || fr.calls != 0 {
			t.Fatalf("%s: want 403 and no upstream call, got %d (calls=%d)", op, rec.Code, fr.calls)
		}
	}
	if rec := postCommand(h, `{"op":"toggle"}`); rec.Code != 200 {
		t.Fatalf("allowed op: %d", rec.Code)
	}
}

func TestCommandTimeoutIs504WithJob(t *testing.T) {
	fr := &fakeRunner{job: &ipc.Job{ID: "j9", State: "running"}, err: context.DeadlineExceeded}
	h := New(commandConfig(t, fr))
	rec := postCommand(h, `{"op":"provider.load_album","params":{"provider":"navidrome","album":"a"}}`)
	if rec.Code != 504 || !strings.Contains(rec.Body.String(), `"j9"`) || !strings.Contains(rec.Body.String(), "job_timeout") {
		t.Fatalf("want 504 job_timeout with the job, got %d %s", rec.Code, rec.Body)
	}
}

// Stream URLs carry the Navidrome login. The browser gets them scrubbed and
// hands the scrubbed path back to play a track; cliamp must get the real one.
func TestStreamCredentialsNeverReachTheBrowser(t *testing.T) {
	const real = "https://nd.lan/rest/stream?c=cliamp&id=abc&s=SALT&t=TOKEN&u=bob"
	fr := &fakeRunner{job: &ipc.Job{State: "succeeded"}}
	cfg := commandConfig(t, fr)
	cfg.Cache.SetState(json.RawMessage(`{"revision":3,"state":"playing","track":{"path":"` + strings.ReplaceAll(real, "&", `&`) + `"}}`))
	h := New(cfg)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/state", nil))
	body := rec.Body.String()
	if strings.Contains(body, "SALT") || strings.Contains(body, "TOKEN") || strings.Contains(body, "bob") {
		t.Fatalf("credentials leaked through /api/state: %s", body)
	}
	var st struct{ Track struct{ Path string } }
	if err := json.Unmarshal([]byte(body), &st); err != nil || st.Track.Path == "" {
		t.Fatalf("state: %v %s", err, body)
	}
	// cliamp's track.play takes a track OBJECT; the path inside must be restored
	if rec := postCommand(h, `{"op":"track.play","params":{"track":{"title":"x","path":`+strconvQuote(st.Track.Path)+`}}}`); rec.Code != 200 {
		t.Fatalf("play: %d", rec.Code)
	}
	tr, _ := fr.lastParams["track"].(map[string]any)
	if tr["path"] != real || tr["title"] != "x" {
		t.Fatalf("cliamp must receive the original URL inside the object, got %v", fr.lastParams["track"])
	}
}

func strconvQuote(s string) string { b, _ := json.Marshal(s); return string(b) }

func TestSettingsOpsAreAllowedWithCheckedParams(t *testing.T) {
	fr := &fakeRunner{job: &ipc.Job{State: "succeeded", Result: json.RawMessage(`{"ok":true}`)}}
	h := New(commandConfig(t, fr))
	for _, ok := range []string{
		`{"op":"device","params":{"name":"list"}}`, `{"op":"device","params":{"name":"alsa_output.usb-Topping_E30"}}`,
		`{"op":"eq","params":{"name":"Rock"}}`, `{"op":"eq","params":{"band":3,"value":-4.5}}`,
		`{"op":"mono","params":{"name":"on"}}`, `{"op":"speed","params":{"value":1.25}}`,
	} {
		if rec := postCommand(h, ok); rec.Code != 200 {
			t.Errorf("%s: %d %s", ok, rec.Code, rec.Body)
		}
	}
	for _, bad := range []string{
		`{"op":"eq","params":{"band":10,"value":1}}`, `{"op":"eq","params":{"band":-1,"value":1}}`, `{"op":"eq","params":{"band":1.5,"value":1}}`,
		`{"op":"eq","params":{"band":1,"value":99}}`, `{"op":"speed","params":{"value":5}}`, `{"op":"speed","params":{"value":0}}`,
		`{"op":"device","params":{"name":123}}`, `{"op":"device","params":{"name":"` + strings.Repeat("a", 201) + `"}}`,
		`{"op":"device","params":{"name":"a\nb"}}`, `{"op":"mono","params":{"path":"/etc/passwd"}}`, `{"op":"mono","params":{"value":1}}`,
	} {
		before := fr.calls
		if rec := postCommand(h, bad); rec.Code != 400 || fr.calls != before {
			t.Errorf("%s: %d (player called: %v)", bad, rec.Code, fr.calls != before)
		}
	}
}

func TestThemeAndVisAreNotOffered(t *testing.T) {
	fr := &fakeRunner{job: &ipc.Job{State: "succeeded"}}
	h := New(commandConfig(t, fr))
	for _, op := range []string{"theme", "vis"} {
		if rec := postCommand(h, `{"op":"`+op+`","params":{"name":"list"}}`); rec.Code != 403 || fr.calls != 0 {
			t.Fatalf("%s: %d", op, rec.Code)
		}
	}
}

func TestAboutReportsBridgeStateWithoutPathsOrLogins(t *testing.T) {
	cfg := guardedConfig(t)
	cfg.Hub = NewHub()
	cfg.About = func() AboutInfo {
		return AboutInfo{Listen: "0.0.0.0:8730", LAN: true, Navidrome: true, MusicRoot: "music", ArtCacheBytes: 1234, ArtCacheMax: 5678}
	}
	_, _ = cfg.Hub.Subscribe()
	rec := artGet(New(cfg), "/api/about", nil)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	b, _ := got["bridge"].(map[string]any)
	if got["name"] != "cliamp-web" || got["clients"] != float64(1) || b["listen"] != "0.0.0.0:8730" || b["art_cache_bytes"] != float64(1234) || b["navidrome"] != true {
		t.Fatalf("%v", got)
	}
	if _, ok := got["player"].(map[string]any)["online"]; !ok {
		t.Fatalf("player state missing: %v", got)
	}
	if strings.Contains(rec.Body.String(), "/home/") || strings.Contains(rec.Body.String(), "password") {
		t.Fatalf("leaks: %s", rec.Body)
	}
}
