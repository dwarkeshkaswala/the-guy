package config

import (
	"os"
	"path/filepath"
	"testing"
)

func setupEnv(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("GUY_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("GUY_PREFS_DIR", filepath.Join(root, "prefs"))
	return root
}

func TestYAMLSubset(t *testing.T) {
	parsed, err := ParseYAMLSubset("# comment\nmax_workers: 3\ndefault_agent: mock\n" +
		"safe_retry_failure_classes:\n  - spawn_error\n  - timeout\n")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if parsed["max_workers"] != 3 {
		t.Fatalf("max_workers: %v", parsed["max_workers"])
	}
	list, ok := parsed["safe_retry_failure_classes"].([]any)
	if !ok || len(list) != 2 || list[0] != "spawn_error" {
		t.Fatalf("list: %v", parsed["safe_retry_failure_classes"])
	}
	if _, err := ParseYAMLSubset("just a bare line with no colon"); err == nil {
		t.Fatal("invalid line accepted")
	}
}

func TestRepoSettingsPrecedence(t *testing.T) {
	setupEnv(t)
	repo := t.TempDir()
	guyDir := filepath.Join(repo, ".guy")
	if err := os.MkdirAll(guyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(guyDir, "project.json"),
		[]byte(`{"max_workers": 3, "default_agent": "mock"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	merged := EffectiveSettings(map[string]any{"max_workers": 4}, repo)
	if v, _ := merged["max_workers"].(int); v != 4 {
		t.Fatalf("CLI override must win, got %v", merged["max_workers"])
	}
	merged2 := EffectiveSettings(nil, repo)
	if v := SettingsInt(merged2, "max_workers", 0); v != 3 {
		t.Fatalf("repo config must beat defaults, got %v", merged2["max_workers"])
	}
	if v, _ := merged2["default_agent"].(string); v != "mock" {
		t.Fatalf("default_agent: %v", merged2["default_agent"])
	}
	if v, _ := merged2["run_timeout_seconds"].(int); v != 1800 {
		t.Fatalf("builtin default expected, got %v", merged2["run_timeout_seconds"])
	}
}

func TestPrefsAndAdapterOverrides(t *testing.T) {
	setupEnv(t)
	if err := SetAdapterOverride("claude", `C:\some path\claude.cmd`); err != nil {
		t.Fatal(err)
	}
	if AdapterOverrides()["claude"] != `C:\some path\claude.cmd` {
		t.Fatalf("override not persisted: %v", AdapterOverrides())
	}
	if err := SetUserSetting("max_workers", 5); err != nil {
		t.Fatal(err)
	}
	if v := SettingsInt(UserSettings(), "max_workers", 0); v != 5 {
		t.Fatalf("user setting not persisted: %v", UserSettings()["max_workers"])
	}
	if err := SetUserSetting("bogus", 1); err == nil {
		t.Fatal("unknown setting accepted")
	}
	if err := SetAdapterOverride("claude", ""); err != nil {
		t.Fatal(err)
	}
	if _, present := AdapterOverrides()["claude"]; present {
		t.Fatal("override not cleared")
	}
	if _, err := os.Stat(PrefsPath()); err != nil {
		t.Fatalf("prefs not written atomically: %v", err)
	}
}
