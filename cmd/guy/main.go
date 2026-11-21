// Command guy is the headless the-guy CLI.
//
// One guy. Every agent. Any repo.
//
// It shares the exact same core as the desktop app (main.go at the module
// root): store, scheduler, adapters, gitops and review all live under
// internal/. This file only wires process-level concerns — the built-in mock
// agent, state directories, the SQLite store, and the build version.
package main

import (
	"fmt"
	"os"

	"github.com/dwarkeshkaswala/the-guy/internal/agents"
	"github.com/dwarkeshkaswala/the-guy/internal/cli"
	"github.com/dwarkeshkaswala/the-guy/internal/config"
	"github.com/dwarkeshkaswala/the-guy/internal/store"
)

// version is stamped by release builds: -ldflags "-X main.version=v0.2.1".
var version string

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if version != "" {
		cli.Version = version
	}

	// Internal subcommand used by the mock adapter, tests and `guy demo`.
	// It must not touch the-guy state: it runs inside agent worktrees.
	if len(args) > 0 && args[0] == "mock-agent" {
		return cli.RunMockAgent(args[1:])
	}

	// Help/version (and bare `guy`) never create directories or a database.
	if len(args) == 0 {
		return cli.Run(args, os.Stdout, os.Stderr, nil)
	}
	switch args[0] {
	case "--help", "-h", "help", "--version", "version":
		return cli.Run(args, os.Stdout, os.Stderr, nil)
	}

	if err := config.EnsureDirs(); err != nil {
		fmt.Fprintf(os.Stderr, "error: initializing the-guy directories: %v\n", err)
		return 1
	}
	st, err := store.Open(config.DBPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: opening the-guy state (%s): %v\n", config.DBPath(), err)
		return 1
	}
	defer st.Close()

	agents.SetBuildVersion(cli.Version)
	return cli.Run(args, os.Stdout, os.Stderr, st)
}
