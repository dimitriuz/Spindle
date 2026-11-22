package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"spindle/internal/enqueue"
)

type fakeEnq struct {
	got     enqueue.Request
	err     error
	starts  int
	cancels int
}

func (f *fakeEnq) Start(r enqueue.Request) error { f.starts++; f.got = r; return f.err }
func (f *fakeEnq) Cancel()                       { f.cancels++ }

func enqServer(t *testing.T, fe *fakeEnq) http.Handler {
	cfg := guardedConfig(t)
	cfg.Enqueue = fe
	cfg.Limiter = NewLimiter(20, 40, time.Now)
	return New(cfg)
}

func enqPost(h http.Handler, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, r)
	return rec
}

func TestEnqueueStartsAndReturns202(t *testing.T) {
	fe := &fakeEnq{}
	rec := enqPost(enqServer(t, fe), "/api/enqueue", `{"kind":"album","provider":"navidrome","id":"al1","mode":"append"}`)
	if rec.Code != 202 || !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if fe.got.Kind != "album" || fe.got.Provider != "navidrome" || fe.got.ID != "al1" || fe.got.Mode != "append" {
		t.Fatalf("request %+v", fe.got)
	}
}

func TestEnqueueTrackCarriesItsObject(t *testing.T) {
	fe := &fakeEnq{}
	enqPost(enqServer(t, fe), "/api/enqueue", `{"kind":"track","mode":"play","track":{"title":"T","path":"https://h/s?id=1"}}`)
	if fe.got.Track["title"] != "T" || fe.got.Track["path"] != "https://h/s?id=1" {
		t.Fatalf("%+v", fe.got.Track)
	}
}

func TestEnqueueErrorStatuses(t *testing.T) {
	for err, want := range map[error]int{enqueue.ErrInvalid: 400, enqueue.ErrUntrusted: 403, enqueue.ErrBusy: 409, errors.New("boom"): 500} {
		rec := enqPost(enqServer(t, &fakeEnq{err: err}), "/api/enqueue", `{"kind":"album","provider":"p","id":"x","mode":"play"}`)
		if rec.Code != want {
			t.Fatalf("%v: want %d, got %d", err, want, rec.Code)
		}
	}
	if rec := enqPost(enqServer(t, &fakeEnq{}), "/api/enqueue", `not json`); rec.Code != 400 {
		t.Fatalf("bad body: %d", rec.Code)
	}
}

func TestEnqueueBusyBodyNamesTheReason(t *testing.T) {
	rec := enqPost(enqServer(t, &fakeEnq{err: enqueue.ErrBusy}), "/api/enqueue", `{"kind":"album","provider":"p","id":"x","mode":"append"}`)
	if !strings.Contains(rec.Body.String(), "busy") {
		t.Fatalf("%s", rec.Body)
	}
}

func TestEnqueueCancel(t *testing.T) {
	fe := &fakeEnq{}
	rec := enqPost(enqServer(t, fe), "/api/enqueue/cancel", `{}`)
	if rec.Code != 200 || fe.cancels != 1 {
		t.Fatalf("%d cancels=%d", rec.Code, fe.cancels)
	}
}

func TestEnqueueIsGuarded(t *testing.T) {
	h := enqServer(t, &fakeEnq{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/enqueue", strings.NewReader(`{}`))) // no JSON type
	if rec.Code != 415 {
		t.Fatalf("content type: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/enqueue", strings.NewReader(`{}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://evil.example")
	h.ServeHTTP(rec, r)
	if rec.Code != 403 {
		t.Fatalf("origin: %d", rec.Code)
	}
}

func TestEnqueueIsRateLimited(t *testing.T) {
	cfg := guardedConfig(t)
	cfg.Enqueue = &fakeEnq{}
	now := time.Unix(1000, 0)
	cfg.Limiter = NewLimiter(1, 2, func() time.Time { return now })
	h := New(cfg)
	codes := []int{}
	for i := 0; i < 4; i++ {
		codes = append(codes, enqPost(h, "/api/enqueue", `{"kind":"album","provider":"p","id":"x","mode":"play"}`).Code)
	}
	if codes[3] != 429 {
		t.Fatalf("burst of 2 then 429, got %v", codes)
	}
}

func TestEnqueueWithoutServiceIs404(t *testing.T) {
	if rec := enqPost(New(guardedConfig(t)), "/api/enqueue", `{}`); rec.Code != 404 {
		t.Fatalf("%d", rec.Code)
	}
}
