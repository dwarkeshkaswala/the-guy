// Package cli implements the guy command line: subcommand dispatch, flag
// parsing (flags may appear anywhere), structured --json output, and exit
// codes (0 ok, 1 error, 2 usage).
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/dwarkeshkaswala/the-guy/internal/store"
)

// Version is the CLI version. It is a var so release builds can stamp it via
// cmd/guy (-ldflags "-X main.version=...").
var Version = "0.2.0-go"

// parsed holds flags (with values) and positionals for one command.
type parsed struct {
	flags      map[string]string
	positional []string
}

func (p *parsed) flag(name string) string { return p.flags[name] }
func (p *parsed) has(name string) bool    { _, ok := p.flags[name]; return ok }
func (p *parsed) bool(name string) bool {
	v, ok := p.flags[name]
	return ok && (v == "" || v == "true" || v == "1")
}
func (p *parsed) arg(i int) string {
	if i < len(p.positional) {
		return p.positional[i]
	}
	return ""
}

// boolFlags so value flags don't swallow the next argument.
var globalBoolFlags = map[string]bool{
	"json": true, "dry-run": true, "approve": true, "follow": true, "purge": true,
	"keep": true, "branch": true, "run-tests": true, "skip-tests": true, "yes": true,
	"check": true, "force": true, "help": true, "tree": true, "all": true,
	"allow-out-of-scope": true,
}

func parseArgs(args []string, boolFlags map[string]bool) parsed {
	out := parsed{flags: map[string]string{}}
	all := map[string]bool{}
	for k, v := range globalBoolFlags {
		all[k] = v
	}
	for k, v := range boolFlags {
		all[k] = v
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			out.positional = append(out.positional, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "--") && len(a) > 2 {
			name := a[2:]
			if eq := strings.Index(name, "="); eq >= 0 {
				out.flags[name[:eq]] = name[eq+1:]
				continue
			}
			if all[name] {
				out.flags[name] = "true"
			} else if i+1 < len(args) {
				out.flags[name] = args[i+1]
				i++
			} else {
				out.flags[name] = "true"
			}
			continue
		}
		out.positional = append(out.positional, a)
	}
	return out
}

// Ctx carries one command invocation.
type Ctx struct {
	Store *store.Store
	JSON  bool
	Out   io.Writer
	Err   io.Writer
}

func (c *Ctx) emit(payload map[string]any, text string) {
	if c.JSON {
		data, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			fmt.Fprintln(c.Out, "{}")
			return
		}
		fmt.Fprintln(c.Out, string(data))
		return
	}
	fmt.Fprintln(c.Out, text)
}

func (c *Ctx) ok(message string, payload map[string]any) int {
	if payload == nil {
		payload = map[string]any{"ok": true}
	}
	c.emit(payload, message)
	return 0
}

type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usage(format string, args ...any) error {
	return &usageError{msg: fmt.Sprintf(format, args...)}
}

type commandFunc func(c *Ctx, p parsed) error

var commands map[string]commandFunc

var usageText = `the-guy — One guy. Every agent. Any repo.

Usage: guy <command> [flags]

Commands:
  init                          initialize the-guy state directories and database
  doctor [--repo R]             environment and agent health check
  agents [list|doctor|set|clear|rescan]
                                agent adapters: capabilities, discovery, configuration
  agents smoke [NAME...] [--approve] [--timeout S]
                                real headless edit test per agent (dry run without --approve)
  repo add <path> [--alias A]   register a git repository
  repo list                     list registered repositories
  repo remove <R> [--purge]     unregister (optionally purge clean worktrees)
  repo use / use [ALIAS]        set/show per-user default repo
  inspect [--repo R]            read-only repository discovery
  plan "objective" [--file F] [--base B] [--agent A] [--planner P]
                                create a DRAFT plan (changes nothing)
  tasks [--plan P]              list tasks and dependencies
  show <TASK>                   full task detail
  approve <TASK|PLAN>           approve for execution, or approve reviewed work
  reject <TASK> --reason R      reject a task in needs_review
  run [--dry-run|--approve] [--task T]... [--plan P] [--max-workers N]
                                launch approved agent tasks in worktrees
  status [--repo R]             tasks, branches, logs, running PIDs
  logs <TASK> [--follow]        show task run logs
  review <TASK> [--run-tests]   diff + test evidence + handoff
  integrate [--dry-run|--approve] [--plan P] [--skip-tests] [--allow-out-of-scope]
                                merge approved tasks into an integration branch
  cancel <TASK>                 cancel a task and kill its process tree
  clean (--task T | --plan P) [--branch]
                                remove worktrees/branches of finished tasks
  config (list|get K|set K V)   view/change user settings
  events [--limit N]            audit trail
  demo [--keep]                 end-to-end demo (disposable repo + mock agents)
  migrate [--from DIR] [--check]
                                adopt Python-implementation state (backup-safe)
  mock-agent                    internal: built-in mock agent (tests/demos only)

Global flags: --json (most commands), --repo <alias|path|id> (repo-scoped commands)
Exit codes: 0 ok, 1 error, 2 usage.
Safety: nothing runs without --approve flags; integration never touches the
default branch; the-guy never pushes.`

// Run executes one CLI invocation and returns the process exit code.
func Run(args []string, stdout, stderr io.Writer, st *store.Store) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usageText)
		return 2
	}
	switch args[0] {
	case "--help", "-h", "help":
		fmt.Fprintln(stdout, usageText)
		return 0
	case "--version", "version":
		fmt.Fprintln(stdout, "the-guy "+Version+" (go)")
		return 0
	}
	handler, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(stderr, "error: unknown command %q\n\n%s\n", args[0], usageText)
		return 2
	}
	p := parseArgs(args[1:], nil)
	if p.has("help") {
		fmt.Fprintln(stdout, usageText)
		return 0
	}
	ctx := &Ctx{Store: st, JSON: p.bool("json"), Out: stdout, Err: stderr}
	err := handler(ctx, p)
	if err == nil {
		return 0
	}
	if _, ok := err.(*silentError); ok {
		return 1 // the command already printed its own (failing) report
	}
	var ue *usageError
	if e, ok := err.(*usageError); ok {
		ue = e
	}
	if ue != nil {
		if ctx.JSON {
			data, _ := json.MarshalIndent(map[string]any{"error": ue.msg}, "", "  ")
			fmt.Fprintln(stdout, string(data))
		} else {
			fmt.Fprintf(stderr, "error: %s\n", ue.msg)
		}
		return 2
	}
	if ctx.JSON {
		data, _ := json.MarshalIndent(map[string]any{"error": err.Error()}, "", "  ")
		fmt.Fprintln(stdout, string(data))
	} else {
		fmt.Fprintf(stderr, "error: %s\n", err.Error())
	}
	return 1
}

func init() {
	commands = map[string]commandFunc{
		"init":      cmdInit,
		"doctor":    cmdDoctor,
		"agents":    cmdAgents,
		"repo":      cmdRepo,
		"use":       cmdUse,
		"inspect":   cmdInspect,
		"plan":      cmdPlan,
		"tasks":     cmdTasks,
		"show":      cmdShow,
		"approve":   cmdApprove,
		"reject":    cmdReject,
		"run":       cmdRun,
		"status":    cmdStatus,
		"logs":      cmdLogs,
		"review":    cmdReview,
		"integrate": cmdIntegrate,
		"cancel":    cmdCancel,
		"clean":     cmdClean,
		"config":    cmdConfig,
		"events":    cmdEvents,
		"demo":      cmdDemo,
		"migrate":   cmdMigrate,
	}
}
