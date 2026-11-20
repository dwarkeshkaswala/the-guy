//go:build windows

package agents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func extOf(path string) string { return strings.ToLower(filepath.Ext(path)) }

func TestExeBeatsCmdBeatsBare(t *testing.T) {
	root := hermetic(t)
	dir := filepath.Join(root, "priority dir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "claude"), []byte("sh stub"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "claude.cmd"), []byte("@echo off"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "claude.exe"), []byte("MZ stub"), 0o644)
	prependPath(t, dir)
	result := Resolve([]string{"claude"}, "GUY_CLAUDE_PATH", "")
	if extOf(result.Chosen) != ".exe" {
		t.Fatalf(".exe must win, chose %s", result.Chosen)
	}
	os.Remove(filepath.Join(dir, "claude.exe"))
	result = Resolve([]string{"claude"}, "GUY_CLAUDE_PATH", "")
	if extOf(result.Chosen) != ".cmd" {
		t.Fatalf(".cmd must be next, chose %s", result.Chosen)
	}
}

func TestPS1OnlyIsNeverChosen(t *testing.T) {
	root := hermetic(t)
	dir := filepath.Join(root, "ps only dir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "claude.ps1"),
		[]byte("Write-Output 'must never run'"), 0o644)
	prependPath(t, dir)
	result := Resolve([]string{"claude"}, "GUY_CLAUDE_PATH", "")
	if result.Chosen != "" || !result.PS1Only {
		t.Fatalf("ps1 must never be chosen: %+v", result)
	}
	report := NewClaude().Probe(defaultProbeTimeout)
	if report.Installed || !strings.Contains(report.HeadlessNote, "PowerShell") ||
		!strings.Contains(report.HeadlessNote, "execution policy") {
		t.Fatalf("ps1 diagnostic: %s", report.HeadlessNote)
	}
}

func TestCmdSiblingPreferredOverPS1Override(t *testing.T) {
	root := hermetic(t)
	dir := filepath.Join(root, "mixed shims")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "claude.ps1"), []byte("Write-Output 'nope'"), 0o644)
	fake := writeFakeCLI(t, dir, "claude", "--print")
	t.Setenv("GUY_CLAUDE_PATH", filepath.Join(dir, "claude.ps1"))
	_ = fake
	result := Resolve([]string{"claude"}, "GUY_CLAUDE_PATH", os.Getenv("GUY_CLAUDE_PATH"))
	if result.Chosen != "" {
		t.Fatalf("ps1 override must be refused, chose %s", result.Chosen)
	}
	if !strings.Contains(result.OverrideError, "execution policy") {
		t.Fatalf("override error: %s", result.OverrideError)
	}
}

func makeFactoryInstall(t *testing.T, base, version string, withBin bool) string {
	t.Helper()
	app := filepath.Join(base, "app-"+version)
	if withBin {
		bin := filepath.Join(app, "resources", "bin")
		if err := os.MkdirAll(bin, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bin, "droid.exe"), []byte("MZ discovery-only stub"), 0o644); err != nil {
			t.Fatal(err)
		}
	} else if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	return app
}

func TestFactoryVersionComparisonNotAlphabetical(t *testing.T) {
	root := hermetic(t)
	fakeLocal := filepath.Join(root, "fake LocalAppData")
	base := filepath.Join(fakeLocal, "Factory")
	for _, v := range []string{"1.2.0", "1.9.0", "1.10.0"} { // alphabetically 1.9 > 1.10
		makeFactoryInstall(t, base, v, true)
	}
	os.MkdirAll(filepath.Join(base, "app-bogus", "resources", "bin"), 0o755)
	makeFactoryInstall(t, base, "0.9.0", false)
	t.Setenv("LOCALAPPDATA", fakeLocal)

	roots := FactoryDroidRoots()
	if len(roots) != 3 {
		t.Fatalf("expected 3 valid roots, got %d", len(roots))
	}
	newest := filepath.Join(base, "app-1.10.0", "resources", "bin")
	if roots[0].Path != newest {
		t.Fatalf("newest must win numerically: %s", roots[0].Path)
	}
	result := Resolve([]string{"droid"}, "GUY_DROID_PATH", "")
	if result.Chosen != filepath.Join(newest, "droid.exe") {
		t.Fatalf("chosen: %s", result.Chosen)
	}
}

func TestFactoryNewestValidWhenNewestBroken(t *testing.T) {
	root := hermetic(t)
	fakeLocal := filepath.Join(root, "fake LocalAppData 2")
	base := filepath.Join(fakeLocal, "Factory")
	makeFactoryInstall(t, base, "9.9.9", false) // newest but no bin
	makeFactoryInstall(t, base, "1.10.0", true)
	makeFactoryInstall(t, base, "1.9.0", true)
	t.Setenv("LOCALAPPDATA", fakeLocal)
	roots := FactoryDroidRoots()
	if roots[0].Path != filepath.Join(base, "app-1.10.0", "resources", "bin") {
		t.Fatalf("broken newest must fall through: %s", roots[0].Path)
	}
}

func TestNPMPrefixResolutionAndDiscovery(t *testing.T) {
	root := hermetic(t)
	prefixDir := filepath.Join(root, "custom npm prefix")
	writeFakeCLI(t, prefixDir, "claude", "--print")
	npmDir := filepath.Join(root, "npm launcher dir")
	if err := os.MkdirAll(npmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(npmDir, "npm.cmd"),
		[]byte("@echo off\r\necho %GUY_FAKE_NPM_PREFIX%\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GUY_FAKE_NPM_PREFIX", prefixDir)
	prependPath(t, npmDir)

	prefix, ok := NPMPrefix()
	if !ok || filepath.Clean(prefix) != filepath.Clean(prefixDir) {
		t.Fatalf("npm prefix: %q %v", prefix, ok)
	}
	result := Resolve([]string{"claude"}, "GUY_CLAUDE_PATH", "")
	if result.Chosen != filepath.Join(prefixDir, "claude.cmd") {
		t.Fatalf("chosen via npm prefix: %s", result.Chosen)
	}
	labels := map[string]string{}
	for _, w := range result.WellKnown {
		labels[w.Root] = w.Label
	}
	if labels[prefixDir] != "npm global prefix" {
		t.Fatalf("label missing for npm prefix root")
	}
}

func TestProbeCacheRediscoveryAndStaleness(t *testing.T) {
	root := hermetic(t)
	basePath := os.Getenv("PATH")
	dirA := filepath.Join(root, "cache dir a")
	fakeA := writeFakeCLI(t, dirA, "claude", "--print")
	t.Setenv("PATH", dirA+string(os.PathListSeparator)+basePath)
	first := reportsByName(ProbeAllCached())
	if first["claude"].Executable != fakeA {
		t.Fatalf("first probe: %s", first["claude"].Executable)
	}
	// cache hit
	second := reportsByName(ProbeAllCached())
	if second["claude"].Executable != fakeA {
		t.Fatal("cache hit failed")
	}
	// PATH change -> fingerprint change -> automatic rediscovery
	dirB := filepath.Join(root, "cache dir b")
	fakeB := writeFakeCLI(t, dirB, "claude", "--print")
	t.Setenv("PATH", dirB+string(os.PathListSeparator)+basePath)
	third := reportsByName(ProbeAllCached())
	if third["claude"].Executable != fakeB {
		t.Fatalf("rediscovery after PATH change: %s", third["claude"].Executable)
	}
	// deleted executable without env change -> re-validated
	os.Remove(fakeB)
	fourth := reportsByName(ProbeAllCached())
	if fourth["claude"].Installed {
		t.Fatal("stale cached executable must be re-validated")
	}
}

func reportsByName(reports []CapabilityReport) map[string]CapabilityReport {
	out := map[string]CapabilityReport{}
	for _, r := range reports {
		out[r.Adapter] = r
	}
	return out
}
