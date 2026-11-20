package agents

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dwkaswala/the-guy/internal/config"
)

// hermetic redirects the-guy state, isolates PATH (keeping git + System32 so
// test helpers keep working), clears APPDATA/LOCALAPPDATA (so the machine's
// real CLIs never leak in), and clears caches.
func hermetic(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("GUY_DATA_DIR", filepath.Join(root, "guy-data"))
	t.Setenv("GUY_PREFS_DIR", filepath.Join(root, "guy-prefs"))
	if err := os.MkdirAll(filepath.Join(root, "guy-data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "guy-prefs"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GUY_VSCODE_EXTENSIONS_DIR", filepath.Join(root, "extensions"))
	_ = os.MkdirAll(filepath.Join(root, "extensions"), 0o755)
	keep := []string{}
	if git, err := exec.LookPath("git"); err == nil {
		keep = append(keep, filepath.Dir(git))
	}
	if runtime.GOOS == "windows" {
		rootDir := os.Getenv("SystemRoot")
		if rootDir == "" {
			rootDir = `C:\Windows`
		}
		keep = append(keep, filepath.Join(rootDir, "System32"))
	}
	empty := filepath.Join(root, "path-empty")
	_ = os.MkdirAll(empty, 0o755)
	t.Setenv("PATH", strings.Join(append([]string{empty}, keep...), string(os.PathListSeparator)))
	// The machine's real installs must not leak into discovery results.
	t.Setenv("APPDATA", filepath.Join(root, "appdata"))
	t.Setenv("LOCALAPPDATA", filepath.Join(root, "localappdata"))
	_ = os.MkdirAll(filepath.Join(root, "appdata"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, "localappdata"), 0o755)
	for _, v := range []string{"GUY_CLAUDE_PATH", "GUY_OPENCODE_PATH", "GUY_CODEX_PATH", "GUY_DROID_PATH"} {
		os.Unsetenv(v)
	}
	ResetNPMPrefixCache()
	ClearProbeCache()
	t.Cleanup(func() { ResetNPMPrefixCache(); ClearProbeCache() })
	return root
}

func writeFakeCLI(t *testing.T, dir, name string, tokens ...string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	helpText := strings.Join(append(tokens, "prompt"), " ")
	var path string
	if runtime.GOOS == "windows" {
		path = filepath.Join(dir, name+".cmd")
		script := "@echo off\r\n" +
			"if \"%1\"==\"--version\" echo fake-cli 1.2.3 & exit /b 0\r\n" +
			"if \"%1\"==\"--help\" echo usage: " + name + " " + helpText + " & exit /b 0\r\n" +
			"echo ok\r\nexit /b 0\r\n"
		if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
			t.Fatal(err)
		}
	} else {
		path = filepath.Join(dir, name)
		script := "#!/bin/sh\ncase \"$1\" in\n" +
			"  --version) echo \"fake-cli 1.2.3\"; exit 0;;\n" +
			"  --help) echo \"usage: " + name + " " + helpText + "\"; exit 0;;\n" +
			"esac\necho ok\nexit 0\n"
		if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func prependPath(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestPathDiscoveryWithSpacesAndProbe(t *testing.T) {
	root := hermetic(t)
	dir := filepath.Join(root, "path dir with spaces")
	fake := writeFakeCLI(t, dir, "claude", "--print")
	prependPath(t, dir)
	report := NewClaude().Probe(defaultProbeTimeout)
	if !report.Installed || !report.HeadlessVerified {
		t.Fatalf("claude not verified: %+v", report)
	}
	if filepath.Clean(report.Executable) != filepath.Clean(fake) {
		t.Fatalf("expected %s, got %s", fake, report.Executable)
	}
	if report.Source != "PATH" {
		t.Fatalf("source: %s", report.Source)
	}
}

func TestOverridesBeatPathAndEnvBeatsConfig(t *testing.T) {
	root := hermetic(t)
	pathDir := filepath.Join(root, "on path")
	writeFakeCLI(t, pathDir, "claude", "--print")
	prependPath(t, pathDir)

	overrideDir := filepath.Join(root, "override dir")
	overrideFake := writeFakeCLI(t, overrideDir, "claude", "--print")
	t.Setenv("GUY_CLAUDE_PATH", overrideFake)
	report := NewClaude().Probe(defaultProbeTimeout)
	if report.Source != "env" {
		t.Fatalf("env override must win, source: %s", report.Source)
	}

	os.Unsetenv("GUY_CLAUDE_PATH")
	if err := config.SetAdapterOverride("claude", overrideFake); err != nil {
		t.Fatal(err)
	}
	report = NewClaude().Probe(defaultProbeTimeout)
	if report.Source != "config" {
		t.Fatalf("config override expected, source: %s", report.Source)
	}
}

func TestStaleOverrideDisablesWithoutFallback(t *testing.T) {
	root := hermetic(t)
	dir := filepath.Join(root, "gone dir")
	fake := writeFakeCLI(t, dir, "claude", "--print")
	t.Setenv("GUY_CLAUDE_PATH", fake)
	os.Remove(fake)
	report := NewClaude().Probe(defaultProbeTimeout)
	if report.Installed || report.Executable != "" {
		t.Fatalf("stale override must disable adapter: %+v", report)
	}
	if !strings.Contains(report.HeadlessNote, "no longer exists") ||
		!strings.Contains(report.HeadlessNote, "guy agents clear") {
		t.Fatalf("diagnostic not actionable: %s", report.HeadlessNote)
	}
}

func TestMissingExecutableReportsSearch(t *testing.T) {
	hermetic(t)
	report := NewClaude().Probe(defaultProbeTimeout)
	if report.Installed {
		t.Fatal("nothing should be installed in hermetic env")
	}
	if !strings.Contains(report.HeadlessNote, "no executable found") ||
		!strings.Contains(report.HeadlessNote, "guy agents doctor") {
		t.Fatalf("note: %s", report.HeadlessNote)
	}
	result := Resolve([]string{"claude"}, "GUY_CLAUDE_PATH", "")
	if result.PathDirsScanned < 1 || len(result.WellKnown) == 0 {
		t.Fatal("search report missing")
	}
}

func TestUnverifiedHelpKeepsAdapterDisabled(t *testing.T) {
	root := hermetic(t)
	dir := filepath.Join(root, "unverified dir")
	writeFakeCLI(t, dir, "claude", "exec-wrong-mode") // help lacks --print
	prependPath(t, dir)
	report := NewClaude().Probe(defaultProbeTimeout)
	if !report.Installed {
		t.Skip("fake CLI not launchable on this platform")
	}
	if report.HeadlessVerified {
		t.Fatal("adapter must stay disabled when --help lacks the token")
	}
	if !strings.Contains(report.HeadlessNote, "refusing to guess") {
		t.Fatalf("note: %s", report.HeadlessNote)
	}
	if _, err := NewClaude().VerifyForExecution(); err == nil {
		t.Fatal("VerifyForExecution must fail")
	}
	_ = report
}

func TestVSCodeExtensionIsOnlyAHint(t *testing.T) {
	root := hermetic(t)
	extDir := os.Getenv("GUY_VSCODE_EXTENSIONS_DIR")
	if err := os.MkdirAll(filepath.Join(extDir, "anthropic.claude-code-1.0.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	report := NewClaude().Probe(defaultProbeTimeout)
	if report.Installed || report.HeadlessVerified {
		t.Fatal("an extension must never count as a CLI")
	}
	if report.ExtensionHint != "anthropic.claude-code-1.0.0" {
		t.Fatalf("extension hint: %q", report.ExtensionHint)
	}
	found := false
	for _, n := range report.Notes {
		if strings.Contains(n, "NOT a global CLI") {
			found = true
		}
	}
	if !found {
		t.Fatal("extension note missing")
	}
	_ = root
}

func TestBuildCommandUsesVerifiedExecutable(t *testing.T) {
	root := hermetic(t)
	dir := filepath.Join(root, "cmd dir")
	fake := writeFakeCLI(t, dir, "codex", "exec")
	t.Setenv("GUY_CODEX_PATH", fake)
	adapter := NewCodex()
	argv, err := adapter.BuildCommand("do the thing", dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{fake, "exec", "do the thing"}
	if strings.Join(argv, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv: %v", argv)
	}
	// prompt is a single argv element; leading '-' is protected
	argv, _ = adapter.BuildCommand("--version", dir, nil)
	if argv[len(argv)-1] != " --version" {
		t.Fatalf("flag-like prompt not protected: %v", argv)
	}
}

func TestMockAdapterUsesSelfBinary(t *testing.T) {
	adapter := NewMock()
	report, err := adapter.VerifyForExecution()
	if err != nil {
		t.Fatal(err)
	}
	if report.Kind != "mock" || !report.HeadlessVerified {
		t.Fatalf("mock must always be ready: %+v", report)
	}
	argv, err := adapter.BuildCommand("p", ".", map[string]any{
		"mock": map[string]any{"write": []any{map[string]any{"path": "x"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if argv[1] != "mock-agent" {
		t.Fatalf("argv: %v", argv)
	}
	env := adapter.ChildEnv(nil, map[string]any{"mock": map[string]any{"exit": 1.0}})
	found := false
	for _, e := range env {
		if strings.HasPrefix(e, "GUY_MOCK_SPEC=") {
			found = true
		}
	}
	if !found {
		t.Fatal("mock spec env missing")
	}
}

const defaultProbeTimeout = 20e9 // 20s in nanoseconds
