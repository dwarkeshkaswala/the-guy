package cli

// `guy agents smoke`: prove each agent CLI can actually EDIT files when run
// the way the-guy runs it (non-interactive, from a subprocess). `--version`
// and `--help` probes only show that a CLI exists; a CLI that needs an extra
// permission/auto-approve flag can exit 0 having changed nothing, which would
// look like success. This runs one tiny, harmless job per agent in a
// throwaway repository outside every registered project.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dwarkeshkaswala/the-guy/internal/agents"
	"github.com/dwarkeshkaswala/the-guy/internal/config"
	"github.com/dwarkeshkaswala/the-guy/internal/gitops"
	"github.com/dwarkeshkaswala/the-guy/internal/process"
	"github.com/dwarkeshkaswala/the-guy/internal/redact"
	"github.com/dwarkeshkaswala/the-guy/internal/tasks"
)

const smokeFile = "guy-smoke.txt"

const smokePrompt = `This is an automated, non-interactive capability check run by the-guy.
In the current directory, create a file named %s whose entire content is this
single line:
%s
Do not modify, create or delete any other file. Do not ask questions.`

// Smoke verdicts (stable strings; also used in --json and events).
const (
	SmokeOK          = "ok"
	SmokeNoEdit      = "no_edit"
	SmokeWrongOutput = "wrong_content"
	SmokeTimeout     = "timeout"
	SmokeFailed      = "failed"
	SmokeUnavailable = "unavailable"
)

type SmokeResult struct {
	Agent      string   `json:"agent"`
	Verdict    string   `json:"verdict"`
	Detail     string   `json:"detail"`
	Hint       string   `json:"hint,omitempty"`
	Command    string   `json:"command,omitempty"`
	ExitCode   *int     `json:"exit_code,omitempty"`
	DurationMS int64    `json:"duration_ms"`
	ExtraFiles []string `json:"extra_files,omitempty"`
	LogPath    string   `json:"log_path,omitempty"`
	KeptRepo   string   `json:"kept_repo,omitempty"`
}

func agentsSmoke(c *Ctx, p parsed) error {
	names := p.positional[1:]
	if len(names) == 0 {
		for _, r := range agents.ProbeAllCached() {
			if r.Kind != "mock" && r.HeadlessVerified {
				names = append(names, r.Adapter)
			}
		}
	}
	for _, n := range names {
		if _, err := agents.Get(n); err != nil {
			return err
		}
	}
	if len(names) == 0 {
		return fmt.Errorf("no verified agent CLI to test; see `guy agents` and `guy agents doctor`")
	}
	timeout := 180
	if v := p.flag("timeout"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 10 {
			return usage("--timeout must be an integer number of seconds (>= 10)")
		}
		timeout = n
	}

	if !p.bool("approve") {
		lines := []string{
			"guy agents smoke — DRY RUN (nothing executed)",
			"",
			"Would run, one agent at a time, a real headless call to: " + strings.Join(names, ", "),
			fmt.Sprintf("Each gets a throwaway git repo under %s and is asked to create %s",
				filepath.Join(config.DataDir(), "smoke"), smokeFile),
			"containing a random token, nothing else. Your projects are never touched.",
			"These are REAL model calls through each CLI's own login (uses its credits/limits).",
			fmt.Sprintf("Timeout per agent: %ds.", timeout),
			"",
			"Run it: guy agents smoke " + strings.Join(p.positional[1:], " ") + " --approve",
		}
		return okErr(c, strings.TrimSpace(strings.Join(lines, "\n")),
			map[string]any{"dry_run": true, "agents": names, "timeout_seconds": timeout})
	}

	var results []SmokeResult
	allOK := true
	for _, name := range names {
		if !c.JSON {
			fmt.Fprintf(c.Out, "smoke: %s … ", name)
		}
		r := runSmoke(name, timeout)
		results = append(results, r)
		if r.Verdict != SmokeOK {
			allOK = false
		}
		if !c.JSON {
			fmt.Fprintln(c.Out, strings.ToUpper(r.Verdict))
		}
		_ = c.Store.AppendEvent("agent_smoke", "", "", map[string]any{
			"agent": r.Agent, "verdict": r.Verdict, "detail": r.Detail,
			"duration_ms": r.DurationMS, "log": r.LogPath})
	}

	lines := []string{""}
	for _, r := range results {
		lines = append(lines, fmt.Sprintf("  %-9s %-14s %s", r.Agent, r.Verdict, r.Detail))
		if r.Command != "" {
			lines = append(lines, "            command: "+r.Command)
		}
		if r.Hint != "" {
			lines = append(lines, "            hint: "+r.Hint)
		}
		if len(r.ExtraFiles) > 0 {
			lines = append(lines, "            also changed: "+summarizeList(r.ExtraFiles, 5))
		}
		if r.LogPath != "" {
			lines = append(lines, "            log: "+r.LogPath)
		}
		if r.KeptRepo != "" {
			lines = append(lines, "            kept for inspection: "+r.KeptRepo)
		}
	}
	payload := map[string]any{"results": results, "ok": allOK}
	text := strings.Join(lines, "\n")
	if !allOK {
		if c.JSON {
			c.emit(payload, "")
		} else {
			fmt.Fprintln(c.Out, text)
		}
		return &silentError{msg: "one or more agents failed the smoke test"}
	}
	return okErr(c, text+"\n\nall tested agents can edit files headlessly", payload)
}

// silentError makes Run exit 1 after the command already printed its output.
type silentError struct{ msg string }

func (e *silentError) Error() string { return e.msg }

func runSmoke(name string, timeout int) (res SmokeResult) {
	res = SmokeResult{Agent: name}
	adapter, err := agents.Get(name)
	if err == nil {
		_, err = adapter.VerifyForExecution()
	}
	if err != nil {
		res.Verdict, res.Detail = SmokeUnavailable, err.Error()
		return res
	}

	token := randomToken()
	stamp := time.Now().UTC().Format("20060102T150405")
	dir := filepath.Join(config.DataDir(), "smoke", fmt.Sprintf("%s-%s-%s", name, stamp, token[:6]))
	if err := initSmokeRepo(dir); err != nil {
		res.Verdict, res.Detail = SmokeFailed, "could not create the throwaway repo: "+err.Error()
		return res
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(dir)
		} else {
			res.KeptRepo = dir
		}
	}()
	baseline, _ := gitops.HeadCommit(dir)

	expected := "guy-smoke-" + token
	prompt := fmt.Sprintf(smokePrompt, smokeFile, expected)
	options := map[string]any{}
	if name == "mock" {
		// Test hook: scripted mock behaviour; {{token}} is substituted.
		if raw := os.Getenv("GUY_MOCK_SMOKE_SPEC"); raw != "" {
			_ = json.Unmarshal([]byte(strings.ReplaceAll(raw, "{{token}}", expected)), &options)
		}
	}
	argv, err := adapter.BuildCommand(prompt, dir, options)
	if err != nil {
		res.Verdict, res.Detail = SmokeUnavailable, err.Error()
		return res
	}
	if len(argv) > 1 {
		res.Command = redact.DisplayArgv(argv[:len(argv)-1]) + " <prompt>"
	}
	res.LogPath = filepath.Join(config.LogsRoot(), "smoke", filepath.Base(dir)+".log")
	outcome := process.Execute(argv, dir, adapter.ChildEnv(os.Environ(), options), timeout,
		res.LogPath, 200_000, nil, nil)
	res.DurationMS = outcome.DurationMS
	if outcome.ExitCodeSet {
		code := outcome.ExitCode
		res.ExitCode = &code
	}

	changed, _ := gitops.ChangedSince(dir, baseline)
	for _, f := range tasks.WorkFiles(name, changed) {
		if f != smokeFile {
			res.ExtraFiles = append(res.ExtraFiles, f)
		}
	}
	content, readErr := os.ReadFile(filepath.Join(dir, smokeFile))

	switch {
	case outcome.FailureClass == "spawn_error":
		res.Verdict, res.Detail = SmokeFailed, "could not start: "+outcome.SpawnError
	case outcome.FailureClass == "timeout":
		res.Verdict = SmokeTimeout
		res.Detail = fmt.Sprintf("no exit within %ds", timeout)
		res.Hint = "a CLI waiting for an interactive approval or login looks like this; run the command above by hand in a scratch folder to see what it waits for"
	case outcome.ExitCode != 0:
		res.Verdict = SmokeFailed
		res.Detail = fmt.Sprintf("exited %d: %s", outcome.ExitCode, lastLines(res.LogPath, 2))
		res.Hint = "often authentication, provider endpoint or rate-limit configuration; see the log"
	case readErr != nil:
		res.Verdict = SmokeNoEdit
		res.Detail = "exited 0 but did not create " + smokeFile
		res.Hint = "in this mode the CLI most likely needs a permission / auto-approve option to edit files " +
			"non-interactively (check its --help); until then the-guy tasks for this agent will finish without changes"
	case strings.TrimSpace(string(content)) != expected:
		res.Verdict = SmokeWrongOutput
		res.Detail = fmt.Sprintf("created %s but with unexpected content", smokeFile)
	default:
		res.Verdict, res.Detail = SmokeOK, fmt.Sprintf("created %s with the expected content", smokeFile)
	}
	keep = res.Verdict != SmokeOK && res.Verdict != SmokeUnavailable
	return res
}

func initSmokeRepo(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	steps := [][]string{
		{"init", "-q"},
		{"-c", "user.email=smoke@the-guy.local", "-c", "user.name=the-guy smoke",
			"commit", "-q", "--allow-empty", "-m", "the-guy smoke baseline"},
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"),
		[]byte("Throwaway repository created by `guy agents smoke`. Safe to delete.\n"), 0o644); err != nil {
		return err
	}
	if _, err := gitops.Git(dir, true, steps[0]...); err != nil {
		return err
	}
	if _, err := gitops.Git(dir, true, "add", "README.md"); err != nil {
		return err
	}
	_, err := gitops.Git(dir, true, steps[1]...)
	return err
}

func randomToken() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b)
}

// lastLines returns the last n non-empty agent-output lines of a log (the
// log is already redacted by the process package).
func lastLines(path string, n int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "(no log)"
	}
	var kept []string
	for _, l := range strings.Split(agentOutput(string(data)), "\n") {
		if strings.TrimSpace(l) != "" {
			kept = append(kept, strings.TrimSpace(l))
		}
	}
	if len(kept) > n {
		kept = kept[len(kept)-n:]
	}
	out := strings.Join(kept, " | ")
	if len(out) > 240 {
		out = out[:240] + "…"
	}
	if out == "" {
		return "(no output)"
	}
	return out
}
