package server

import (
	"strings"
	"testing"
)

const navURL = "https://nd.lan/rest/stream?c=cliamp&f=json&id=abc&s=SALT&t=TOKEN&u=bob&v=1.0.0"

func TestScrubRemovesSecretsAndRestoreReturnsOriginal(t *testing.T) {
	r := NewRedactor()
	in := `{"track":{"title":"x","path":"` + strings.ReplaceAll(navURL, "&", `&`) + `","duration_secs":291},"n":[1,2.5]}`
	out := string(r.Scrub([]byte(in)))
	for _, secret := range []string{"SALT", "TOKEN", "u=bob"} {
		if strings.Contains(out, secret) {
			t.Fatalf("secret %q leaked: %s", secret, out)
		}
	}
	if !strings.Contains(out, "id=abc") || !strings.Contains(out, `"duration_secs":291`) || !strings.Contains(out, "2.5") {
		t.Fatalf("non-secret data must survive intact: %s", out)
	}
	scrubbed, _ := scrubURL(navURL)
	if got := r.Restore(scrubbed); got != navURL {
		t.Fatalf("restore: %s", got)
	}
	if got := r.Restore("https://other/x"); got != "https://other/x" {
		t.Fatalf("unknown strings pass through: %s", got)
	}
}

func TestScrubLeavesPlainJSONAlone(t *testing.T) {
	r := NewRedactor()
	in := []byte(`{"path":"/music/a.flac","u":"bob","url":"https://radio.example/stream"}`)
	if string(r.Scrub(in)) != string(in) {
		t.Fatal("bodies without secret URL params must be returned untouched")
	}
}

func TestRedactorEvictsOldest(t *testing.T) {
	r := NewRedactor()
	for i := 0; i < redactorCap+5; i++ {
		r.remember("s"+itoa(i), "o"+itoa(i))
	}
	if len(r.orig) != redactorCap {
		t.Fatalf("size %d", len(r.orig))
	}
	if r.Restore("s0") != "s0" || r.Restore("s"+itoa(redactorCap+4)) != "o"+itoa(redactorCap+4) {
		t.Fatal("oldest must be evicted, newest kept")
	}
}

func itoa(i int) string {
	const d = "0123456789"
	if i == 0 {
		return "0"
	}
	s := ""
	for ; i > 0; i /= 10 {
		s = string(d[i%10]) + s
	}
	return s
}
