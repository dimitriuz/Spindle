package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var testHTTP = &http.Client{Timeout: 2 * time.Second}

// startFakePlayer answers the V2 methods the bridge uses at startup with a
// fixed "playing" world: enough to prove the wiring end to end.
func startFakePlayer(t *testing.T) string {
	dir := t.TempDir()
	path := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				sc := bufio.NewScanner(bufio.NewReader(c))
				for sc.Scan() {
					var req map[string]json.RawMessage
					if json.Unmarshal(sc.Bytes(), &req) != nil {
						continue
					}
					var id, method string
					_ = json.Unmarshal(req["id"], &id)
					_ = json.Unmarshal(req["method"], &method)
					switch method {
					case "subscribe":
						fmt.Fprintf(c, "{\"version\":2,\"id\":%q,\"ok\":true}\n", id)
						fmt.Fprint(c, `{"event":"runtime.state","seq":1,"retained":true,"data":{"state":"playing","revision":5,"playlist_revision":7,"index":0,"track":{"title":"Fake Song"}}}`+"\n")
					case "state.get":
						fmt.Fprintf(c, "{\"version\":2,\"id\":%q,\"ok\":true,\"snapshot\":{\"state\":\"playing\",\"revision\":5,\"playlist_revision\":7,\"track\":{\"title\":\"Fake Song\"}}}\n", id)
					case "capabilities":
						fmt.Fprintf(c, "{\"version\":2,\"id\":%q,\"ok\":true,\"result\":[{\"name\":\"toggle\"},{\"name\":\"queue.list\"}]}\n", id)
					case "operation.submit":
						// real cliamp validates the envelope: "operation" must be
						// top-level, a nested one is invalid_request.
						var op string
						_ = json.Unmarshal(req["operation"], &op)
						if op == "" {
							fmt.Fprintf(c, "{\"version\":2,\"id\":%q,\"ok\":false,\"error\":{\"code\":\"invalid_request\",\"message\":\"invalid request\"}}\n", id)
							break
						}
						result := `{"ok":true,"total":1,"tracks":[{"path":"/m/a.flac","title":"Fake Song"}]}`
						if op == "playnext.list" {
							result = `{"ok":true}`
						}
						fmt.Fprintf(c, "{\"version\":2,\"id\":%q,\"ok\":true,\"job\":{\"id\":\"j1\",\"operation\":%q,\"state\":\"succeeded\",\"result\":%s}}\n", id, op, result)
					default:
						fmt.Fprintf(c, "{\"version\":2,\"id\":%q,\"ok\":true}\n", id)
					}
				}
				c.Close()
			}()
		}
	}()
	return path
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

func readStatus(t *testing.T, dir string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(filepath.Join(dir, "status.json"))
		if err == nil {
			var m map[string]any
			if json.Unmarshal(b, &m) == nil {
				return m
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("status.json never appeared")
	return nil
}

func getJSON(t *testing.T, url, tok string) map[string]any {
	t.Helper()
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Token", tok)
	resp, err := testHTTP.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s: %d", url, resp.StatusCode)
	}
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRunWiresFakePlayer(t *testing.T) {
	sock := startFakePlayer(t)
	dir := t.TempDir()
	port := freePort(t)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- run(ctx, runConfig{Socket: sock, Port: port, DataDir: dir, Bind: "127.0.0.1"}) }()

	st := readStatus(t, dir)
	tok, _ := st["token"].(string)
	if tok == "" {
		t.Fatalf("status.json has no token: %v", st)
	}
	if p, _ := st["port"].(float64); int(p) != port {
		t.Fatalf("status port %v != %d", st["port"], port)
	}
	if fi, err := os.Stat(filepath.Join(dir, "status.json")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("status.json mode: %v %v", fi, err)
	}

	base := fmt.Sprintf("http://127.0.0.1:%d", port)

	// state prefetched through subscriber retention or state.get
	var snap map[string]any
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		snap = getJSON(t, base+"/api/state", tok)
		if snap["state"] == "playing" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if snap["state"] != "playing" {
		t.Fatalf("state never prefetched: %v", snap)
	}

	q := getJSON(t, base+"/api/queue", tok)
	tracks, _ := q["tracks"].([]any)
	if len(tracks) != 1 {
		t.Fatalf("queue never prefetched: %v", q)
	}

	caps := getJSON(t, base+"/api/capabilities", tok)
	if ops, _ := caps["result"].([]any); len(ops) != 2 {
		t.Fatalf("capabilities envelope not passed through: %v", caps)
	}

	// command path reaches the fake player
	resp := postCommand(t, base, tok, `{"op":"toggle"}`)
	if resp["job"] == nil {
		t.Fatalf("command got no job: %v", resp)
	}

	// health reports the player online
	h := getJSONNoAuth(t, base+"/api/health")
	if h["player"] != "online" {
		t.Fatalf("health: %v", h)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("run returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("run did not exit after ctx cancel")
	}
}

func postCommand(t *testing.T, base, tok, body string) map[string]any {
	t.Helper()
	r, err := http.NewRequest("POST", base+"/api/command", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("X-Token", tok)
	resp, err := testHTTP.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	json.NewDecoder(resp.Body).Decode(&m)
	return m
}

func getJSONNoAuth(t *testing.T, url string) map[string]any {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	json.NewDecoder(resp.Body).Decode(&m)
	return m
}
