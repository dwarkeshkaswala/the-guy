// Package agents defines the adapter interface, the concrete adapters
// (Claude Code, OpenCode, Codex CLI, Factory Droid CLI, built-in mock) and
// the capability-checked registry. Agent features are verified against real
// `--help` output — never assumed from `--version`. the-guy never adds
// permission-bypass flags and never silently falls back to another agent.
package agents

import (
	"fmt"
	"os"
	"time"

	"github.com/dwarkeshkaswala/the-guy/internal/config"
)

type AdapterUnavailable struct{ Msg string }

func (e *AdapterUnavailable) Error() string { return e.Msg }

// CapabilityReport is the result of a read-only probe.
type CapabilityReport struct {
	Adapter          string         `json:"adapter"`
	Display          string         `json:"display"`
	Kind             string         `json:"kind"`
	Installed        bool           `json:"installed"`
	Executable       string         `json:"executable"`
	Source           string         `json:"source"`
	Version          string         `json:"version"`
	HeadlessVerified bool           `json:"headless_verified"`
	HeadlessNote     string         `json:"headless_note"`
	InstallHint      string         `json:"install_hint,omitempty"`
	Notes            []string       `json:"notes"`
	ExtensionHint    string         `json:"extension_hint"`
	Discovery        map[string]any `json:"discovery"`
}

// Adapter is the required agent interface (Discover/Probe/Capabilities/
// BuildCommand/Run/Cancel are realized as Probe, BuildCommand, ChildEnv plus
// process supervision in internal/process).
type Adapter interface {
	Name() string
	Display() string
	Kind() string
	EnvVar() string
	InstallHint() string
	Probe(timeout time.Duration) CapabilityReport
	VerifyForExecution() (CapabilityReport, error)
	BuildCommand(prompt string, cwd string, options map[string]any) ([]string, error)
	ChildEnv(base []string, options map[string]any) []string
}

// Base implements shared behavior; concrete adapters embed it.
type Base struct {
	AdapterName        string
	AdapterDisplay     string
	AdapterKind        string
	Candidates         []string
	VerifyTokens       []string
	Subcommand         []string
	AdapterInstallHint string
}

func (b *Base) Name() string    { return b.AdapterName }
func (b *Base) Display() string { return b.AdapterDisplay }
func (b *Base) Kind() string {
	if b.AdapterKind == "" {
		return "real"
	}
	return b.AdapterKind
}
func (b *Base) EnvVar() string      { return "GUY_" + upper(b.AdapterName) + "_PATH" }
func (b *Base) InstallHint() string { return b.AdapterInstallHint }

func upper(s string) string {
	out := []rune(s)
	for i, r := range out {
		if r >= 'a' && r <= 'z' {
			out[i] = r - 32
		}
	}
	return string(out)
}

func (b *Base) Probe(timeout time.Duration) CapabilityReport {
	overrides := config.AdapterOverrides()
	result := Resolve(b.Candidates, b.EnvVar(), overrides[b.AdapterName])
	report := CapabilityReport{
		Adapter:     b.AdapterName,
		Display:     b.AdapterDisplay,
		Kind:        b.Kind(),
		InstallHint: b.AdapterInstallHint,
		Notes:       []string{},
		Discovery:   ResultToMap(result),
	}
	if b.Kind() == "real" {
		if hit := VSCodeExtensionHit(b.AdapterName); hit != "" {
			report.ExtensionHint = hit
			report.Notes = append(report.Notes,
				fmt.Sprintf("VS Code extension '%s' detected — an extension is NOT a global CLI and does not enable this adapter; install the CLI itself.", hit))
		}
	}
	if result.OverrideError != "" {
		report.HeadlessNote = result.OverrideError
		return report
	}
	if result.Chosen == "" {
		if result.PS1Only {
			report.HeadlessNote = PS1Note
		} else {
			report.HeadlessNote = fmt.Sprintf(
				"no executable found for '%s' on PATH or in common install locations — run `guy agents doctor %s` for the full search report",
				b.Candidates[0], b.AdapterName)
		}
		return report
	}
	report.Installed = true
	report.Executable = result.Chosen
	report.Source = result.ChosenSource

	if out, ok := runCapture([]string{result.Chosen, "--version"}, timeout); ok && out != "" {
		report.Version = firstLine(out)
	}
	helpText := ""
	if out, ok := runCapture([]string{result.Chosen, "--help"}, timeout); ok {
		helpText = out
	}
	missing := []string{}
	for _, tok := range b.VerifyTokens {
		if !contains(helpText, tok) {
			missing = append(missing, tok)
		}
	}
	if len(b.VerifyTokens) == 0 {
		report.HeadlessNote = "no verification tokens configured; adapter disabled"
	} else if len(missing) > 0 {
		report.HeadlessNote = fmt.Sprintf(
			"--help does not mention %s; refusing to guess CLI syntax (adapter disabled — update the adapter for this CLI version)",
			joinList(missing))
	} else {
		report.HeadlessVerified = true
		report.HeadlessNote = "verified " + joinList(b.VerifyTokens) + " in --help"
		if len(b.Subcommand) > 0 {
			argv := append([]string{result.Chosen}, b.Subcommand...)
			argv = append(argv, "--help")
			if _, ok := runCapture(argv, timeout); !ok {
				report.Notes = append(report.Notes,
					fmt.Sprintf("`%s --help` probe failed; verification relies on top-level --help evidence", joinList(b.Subcommand)))
			}
		}
	}
	report.Notes = append(report.Notes,
		"Model/provider/auth stay exactly as configured in the CLI itself; the-guy never overrides them.",
		"the-guy never adds permission-bypass flags; if the CLI asks for permission the run will time out.")
	return report
}

func (b *Base) VerifyForExecution() (CapabilityReport, error) {
	report := b.Probe(20 * time.Second)
	if !report.Installed || !report.HeadlessVerified {
		note := report.HeadlessNote
		if note == "" {
			note = "not installed"
		}
		return report, &AdapterUnavailable{Msg: fmt.Sprintf("adapter '%s' unavailable: %s", b.AdapterName, note)}
	}
	return report, nil
}

func (b *Base) ChildEnv(base []string, _ map[string]any) []string { return base }

// PromptArg passes prompts as a single argv element (shell metacharacters are
// inert). A leading "-" would parse as a flag for some CLIs, so prefix it
// with a space in that pathological case.
func PromptArg(prompt string) string {
	if len(prompt) > 0 && prompt[0] == '-' {
		return " " + prompt
	}
	return prompt
}

// ---- concrete adapters ----

type ClaudeAdapter struct{ Base }

func NewClaude() *ClaudeAdapter {
	return &ClaudeAdapter{Base{AdapterName: "claude", AdapterDisplay: "Claude Code",
		Candidates:         []string{"claude"},
		VerifyTokens:       []string{"--print"},
		AdapterInstallHint: "install the Claude Code CLI (e.g. `npm install -g @anthropic-ai/claude-code` or the official native installer), then run `guy agents rescan` — or point directly at it with `guy agents set claude <path-to-claude.exe>`"}}
}

func (a *ClaudeAdapter) BuildCommand(prompt, cwd string, _ map[string]any) ([]string, error) {
	report, err := a.VerifyForExecution()
	if err != nil {
		return nil, err
	}
	return []string{report.Executable, "-p", PromptArg(prompt)}, nil
}

type OpenCodeAdapter struct{ Base }

func NewOpenCode() *OpenCodeAdapter {
	return &OpenCodeAdapter{Base{AdapterName: "opencode", AdapterDisplay: "OpenCode",
		Candidates:         []string{"opencode"},
		VerifyTokens:       []string{"run"},
		Subcommand:         []string{"run"},
		AdapterInstallHint: "install the OpenCode CLI (see opencode.ai docs), then run `guy agents rescan` — or `guy agents set opencode <path>`"}}
}

func (a *OpenCodeAdapter) BuildCommand(prompt, cwd string, _ map[string]any) ([]string, error) {
	report, err := a.VerifyForExecution()
	if err != nil {
		return nil, err
	}
	return []string{report.Executable, "run", PromptArg(prompt)}, nil
}

type CodexAdapter struct{ Base }

func NewCodex() *CodexAdapter {
	return &CodexAdapter{Base{AdapterName: "codex", AdapterDisplay: "Codex CLI",
		Candidates:         []string{"codex"},
		VerifyTokens:       []string{"exec"},
		Subcommand:         []string{"exec"},
		AdapterInstallHint: "install the Codex CLI (e.g. `npm install -g @openai/codex` or brew), then run `guy agents rescan` — or `guy agents set codex <path>`"}}
}

func (a *CodexAdapter) BuildCommand(prompt, cwd string, _ map[string]any) ([]string, error) {
	report, err := a.VerifyForExecution()
	if err != nil {
		return nil, err
	}
	return []string{report.Executable, "exec", PromptArg(prompt)}, nil
}

type DroidAdapter struct{ Base }

func NewDroid() *DroidAdapter {
	return &DroidAdapter{Base{AdapterName: "droid", AdapterDisplay: "Factory Droid CLI",
		Candidates:         []string{"droid"},
		VerifyTokens:       []string{"exec"},
		Subcommand:         []string{"exec"},
		AdapterInstallHint: "install the Factory Droid CLI per Factory's docs (a `droid` launcher on PATH or under a common location), then run `guy agents rescan`"}}
}

func (a *DroidAdapter) BuildCommand(prompt, cwd string, _ map[string]any) ([]string, error) {
	report, err := a.VerifyForExecution()
	if err != nil {
		return nil, err
	}
	return []string{report.Executable, "exec", PromptArg(prompt)}, nil
}

// MockAdapter runs the binary's built-in mock agent (`guy mock-agent`),
// fully deterministic, zero model cost. Tests can point GUY_MOCK_AGENT_BIN at
// any binary that accepts the same subcommand.
type MockAdapter struct{ Base }

func NewMock() *MockAdapter {
	return &MockAdapter{Base{AdapterName: "mock", AdapterDisplay: "Mock agent (built-in)",
		AdapterKind:        "mock",
		Candidates:         []string{},
		AdapterInstallHint: "built-in; nothing to install"}}
}

func (a *MockAdapter) Probe(_ time.Duration) CapabilityReport {
	self := "guy mock-agent (built-in)"
	if exe, err := os.Executable(); err == nil {
		self = exe + " mock-agent"
	}
	if bin := os.Getenv("GUY_MOCK_AGENT_BIN"); bin != "" {
		self = bin
	}
	return CapabilityReport{
		Adapter: "mock", Display: "Mock agent (built-in)", Kind: "mock",
		Installed: true, Executable: self, Version: "built-in",
		HeadlessVerified: true, HeadlessNote: "built-in; deterministic",
		Notes: []string{"Tests/demos only — does not call any model provider."},
	}
}

func (a *MockAdapter) VerifyForExecution() (CapabilityReport, error) { return a.Probe(0), nil }

func (a *MockAdapter) BuildCommand(prompt, cwd string, _ map[string]any) ([]string, error) {
	runner, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if bin := os.Getenv("GUY_MOCK_AGENT_BIN"); bin != "" {
		runner = bin
	}
	return []string{runner, "mock-agent", "--prompt", PromptArg(prompt)}, nil
}

func (a *MockAdapter) ChildEnv(base []string, options map[string]any) []string {
	env := append([]string{}, base...)
	spec := options
	if m, ok := options["mock"].(map[string]any); ok {
		spec = m
	}
	if spec == nil {
		spec = map[string]any{}
	}
	if data, err := marshalJSON(spec); err == nil {
		env = append(env, "GUY_MOCK_SPEC="+string(data))
	}
	return env
}

// ---- registry ----

var registry = map[string]Adapter{}
var registryOrder []string

func Register(a Adapter) {
	if _, exists := registry[a.Name()]; !exists {
		registryOrder = append(registryOrder, a.Name())
	}
	registry[a.Name()] = a
}

func Get(name string) (Adapter, error) {
	if a, ok := registry[name]; ok {
		return a, nil
	}
	names := append([]string{}, registryOrder...)
	return nil, &AdapterUnavailable{Msg: fmt.Sprintf("unknown agent '%s'; available: %s", name, joinList(names))}
}

func All() []Adapter {
	out := make([]Adapter, 0, len(registryOrder))
	for _, name := range registryOrder {
		out = append(out, registry[name])
	}
	return out
}

func init() {
	Register(NewMock())
	Register(NewClaude())
	Register(NewOpenCode())
	Register(NewCodex())
	Register(NewDroid())
}
