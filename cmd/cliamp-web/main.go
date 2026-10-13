package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"cliamp-web/internal/ipc"
	"cliamp-web/internal/server"
	"cliamp-web/internal/snapshot"
	"cliamp-web/webui"
)

type runConfig struct {
	Socket       string
	Port         int
	Bind         string
	DataDir      string
	AllowedHosts []string
	Verbose      bool
}

func run(ctx context.Context, rc runConfig) error {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if rc.Verbose {
		logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	tok, err := server.LoadOrCreateToken(rc.DataDir)
	if err != nil {
		return err
	}

	cache := snapshot.NewCache()
	hub := server.NewHub()
	conn := ipc.NewConn(rc.Socket)
	defer conn.Close()

	refresh := func() {
		if resp, err := conn.Call(ctx, "state.get", nil); err == nil && len(resp.Snapshot) > 0 {
			cache.SetState(resp.Snapshot)
		} else if err != nil && ctx.Err() == nil {
			logger.Warn("state.get", "err", err)
		}
		if caps, err := conn.Call(ctx, "capabilities", nil); err == nil {
			cache.SetCaps(caps.Raw)
		}
		for _, op := range []string{"queue.list", "playnext.list"} {
			job, err := conn.SubmitJob(ctx, op, nil)
			if err != nil {
				logger.Warn(op, "err", err)
				continue
			}
			if op == "queue.list" {
				cache.AbsorbQueueResult(job.Result, cache.PlaylistRevision())
			} else {
				cache.AbsorbPlayNextResult(job.Result, cache.PlaylistRevision())
			}
		}
	}

	sub := ipc.NewSubscriber(rc.Socket,
		[]string{"runtime.state", "runtime.playback", "runtime.playlist", "runtime.settings", "runtime.job"},
		func(ev ipc.Event) {
			switch cache.Apply(ev) {
			case "state", "settings":
				hub.Broadcast("state", cache.State())
			case "playback":
				hub.Broadcast("playback", ev.Data)
			case "playlist":
				// the browser refetches /api/queue on this signal, so the
				// queue cache must already hold the new rows: absorb first,
				// then ring the bell.
				go func() {
					refresh()
					hub.Broadcast("playlist", ev.Data)
				}()
			case "job":
				hub.Broadcast("job", ev.Data)
			}
		},
		func(on bool) {
			cache.SetOnline(on)
			status := "offline"
			if on {
				status = "online"
			}
			hub.Broadcast("player", map[string]string{"status": status})
			if on {
				go func() {
					refresh()
					hub.Broadcast("resync", map[string]string{})
				}()
			}
		})
	go sub.Run(ctx)


	h := server.New(server.Config{
		Cache: cache, Runner: conn, Hub: hub,
		Auth: server.NewAuth(tok), Guard: server.NewHostGuard(rc.AllowedHosts),
		Limiter: server.NewLimiter(20, 40, time.Now), UI: webui.FS(), Logger: logger,
	})

	if err := writeStatus(rc.DataDir, tok, rc.Port, rc.Bind); err != nil {
		logger.Warn("status.json", "err", err)
	}

	addr := net.JoinHostPort(rc.Bind, strconv.Itoa(rc.Port))
	srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 5 * time.Second}
	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()
	logger.Info("cliamp-web serving", "addr", addr, "socket", rc.Socket)

	select {
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
		return nil
	case err := <-errCh:
		return err
	}
}

func writeStatus(dir, tok string, port int, bind string) error {
	hostport := net.JoinHostPort(bind, strconv.Itoa(port))
	b, err := json.Marshal(map[string]any{
		"url":        "http://" + hostport + "/?token=" + tok,
		"token":      tok,
		"port":       port,
		"pid":        os.Getpid(),
		"started_at": time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	tmp := dir + "/status.json.tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, dir+"/status.json")
}

func defaultSocket() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return d + "/cliamp/cliamp.sock"
	}
	return os.Getenv("HOME") + "/.config/cliamp/cliamp.sock"
}

func defaultDataDir() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return d + "/cliamp/web"
	}
	return os.Getenv("HOME") + "/.local/share/cliamp/web"
}

func main() {
	if len(os.Args) < 2 || os.Args[1] != "serve" {
		fmt.Fprintln(os.Stderr, "usage: cliamp-web serve [--port N] [--bind ADDR] [--lan] [--socket PATH] [--data-dir DIR] [--allowed-hosts LIST] [--verbose]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	rc := runConfig{}
	fs.IntVar(&rc.Port, "port", 8730, "HTTP listen port")
	fs.StringVar(&rc.Bind, "bind", "127.0.0.1", "bind address")
	lan := fs.Bool("lan", false, "bind 0.0.0.0 (LAN-visible)")
	fs.StringVar(&rc.Socket, "socket", defaultSocket(), "cliamp IPC socket path")
	fs.StringVar(&rc.DataDir, "data-dir", defaultDataDir(), "bridge data directory")
	hosts := fs.String("allowed-hosts", "", "comma-separated extra allowed Host values")
	fs.BoolVar(&rc.Verbose, "verbose", false, "verbose logging to stderr")
	_ = fs.Parse(os.Args[2:])
	if *lan {
		rc.Bind = "0.0.0.0"
	}
	for _, hh := range strings.Split(*hosts, ",") {
		if hh = strings.TrimSpace(hh); hh != "" {
			rc.AllowedHosts = append(rc.AllowedHosts, hh)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, rc); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
