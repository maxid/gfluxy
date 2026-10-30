// Copyright 2026 The gfluxy Authors.
//
// Command gfluxy is the northbound traffic gateway: one listener that speaks
// HTTP/1.1, HTTP/2, HTTP/3 and WebSocket, routes on the frozen `/api/*`
// prefixes, discovers upstreams through a registry (static file or Nacos) and
// splits traffic by header, tenant and percentage.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/maxid/gfluxy/internal/config"
	"github.com/maxid/gfluxy/internal/server"
	"github.com/maxid/gfluxy/internal/version"
)

func main() {
	showVersion := flag.Bool("version", false, "print build identity and exit")
	configPath := flag.String("config", "", "path to the start-up configuration file")
	flag.Parse()

	if *showVersion {
		fmt.Println(version.String())
		return
	}

	if err := run(*configPath); err != nil {
		fmt.Fprintf(os.Stderr, "gfluxy: %v\n", err)
		os.Exit(1)
	}
}

func run(path string) error {
	if path == "" {
		return fmt.Errorf("-config is required")
	}

	cfg, err := config.Load(path)
	if err != nil {
		return err
	}

	srv, err := server.New(cfg)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := srv.Start(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "gfluxy: listening on %s\n", cfg.Listen.Addr)

	<-ctx.Done()
	return srv.Stop()
}
