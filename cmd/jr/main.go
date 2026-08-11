// Command jr is a deterministic Jira client for scripts and agents.
//
// This file contains no logic beyond process wiring. Everything it can do comes
// from the command registry, and which commands are in the registry is decided
// at compile time by build tags. See docs/build-profiles.md.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/kmoneil/jr/internal/cli"

	// Links every resource into the binary. A resource registers itself from
	// an init function in a tag-gated file, so which ones are present is
	// decided at compile time.
	_ "github.com/kmoneil/jr/internal/commands"
)

func main() {
	// A cancelled context unwinds in-flight requests instead of killing the
	// process mid-write, so a partial result still reports itself as partial.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	code := cli.Main(ctx, os.Args[1:], cli.Options{
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		Getenv: os.Getenv,
	})
	os.Exit(code.Int())
}
