// Command guy is the the-guy orchestrator CLI (Go implementation).
//
// One guy. Every agent. Any repo.
package main

import (
	"os"

	"github.com/dwkaswala/the-guy/internal/cli"
)

func main() {
	// Hidden internal subcommand: the built-in mock agent used by tests/demos.
	if len(os.Args) > 1 && os.Args[1] == "mock-agent" {
		os.Exit(cli.RunMockAgent(os.Args[2:]))
	}
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if err := initState(); err != nil {
		os.Stderr.WriteString("error: " + err.Error() + "\n")
		return 1
	}
	st, err := openStore()
	if err != nil {
		os.Stderr.WriteString("error: " + err.Error() + "\n")
		return 1
	}
	defer st.Close()
	return cli.Run(args, os.Stdout, os.Stderr, st)
}
