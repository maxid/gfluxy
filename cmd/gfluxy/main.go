// Copyright 2026 The gfluxy Authors.
//
// Command gfluxy is the northbound traffic gateway: one listener that speaks
// HTTP/1.1, HTTP/2, HTTP/3 and WebSocket, routes on the frozen `/api/*`
// prefixes, discovers upstreams through a registry (static file or Nacos) and
// splits traffic by header, tenant and percentage.
//
// The server is wired in later milestones; this entry point only reports the
// build identity until then.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/maxid/gfluxy/internal/version"
)

func main() {
	showVersion := flag.Bool("version", false, "print build identity and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version.String())
		return
	}

	fmt.Fprintln(os.Stderr, "gfluxy: listener not wired yet; run with -version")
	os.Exit(1)
}
