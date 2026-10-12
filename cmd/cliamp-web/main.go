package main

import (
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"

	"cliamp-web/internal/server"
	"cliamp-web/internal/snapshot"
	"cliamp-web/webui"
)

func defaultDataDir() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return d + "/cliamp/web"
	}
	return os.Getenv("HOME") + "/.local/share/cliamp/web"
}

func main() {
	if len(os.Args) < 2 || os.Args[1] != "serve" {
		fmt.Fprintln(os.Stderr, "usage: cliamp-web serve [--port N] [--lan] [--data-dir DIR] [--verbose] [--allowed-hosts LIST] [--socket PATH] [--bind ADDR]")
		os.Exit(2)
	}
	// flag parsing + full wiring arrives in Task 8; this skeleton only
	// starts an HTTP server answering /api/health.
	port := os.Getenv("CLIAMP_WEB_PORT") // Task 8 replaces with real flags
	if port == "" {
		port = "8730"
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cache := snapshot.NewCache()
	static, err := fs.Sub(webui.Dist, "dist")
	if err != nil {
		logger.Error("static", "err", err)
		os.Exit(1)
	}
	h := server.New(server.Config{Cache: cache, Hub: server.NewHub(), Static: static, Logger: logger})
	addr := "127.0.0.1:" + port
	logger.Info("listening", "addr", addr)
	if err := http.ListenAndServe(addr, h); err != nil {
		logger.Error("serve", "err", err)
		os.Exit(1)
	}
}
