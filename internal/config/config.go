// Package config handles OS-aware paths, user preferences, settings
// precedence, per-repo optional configuration, and adapter executable
// overrides. Runtime state lives OUTSIDE target repositories:
//
//	Windows: data %LOCALAPPDATA%\the-guy, prefs %APPDATA%\the-guy
//	POSIX:   ~/.local/share/the-guy and ~/.config/the-guy (XDG honored)
//
// GUY_DATA_DIR / GUY_PREFS_DIR override everything (used heavily by tests).
// The prefs.json and SQLite formats are shared with the Python implementation
// so both CLIs can operate on the same state during migration.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const AppName = "the-guy"

// Defaults mirrors the Python implementation exactly.
var Defaults = map[string]any{
	"max_workers":                2,
	"default_agent":              nil,
	"run_timeout_seconds":        1800,
	"planner_timeout_seconds":    300,
	"log_max_bytes":              1_000_000,
	"log_retention_days":         30,
	"safe_retry_failure_classes": []string{"spawn_error"},
}

func DataDir() string {
	if env := os.Getenv("GUY_DATA_DIR"); env != "" {
		return env
	}
	if runtime.GOOS == "windows" {
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			base = filepath.Join(homeDir(), "AppData", "Local")
		}
		return filepath.Join(base, AppName)
	}
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, AppName)
	}
	return filepath.Join(homeDir(), ".local", "share", AppName)
}

func PrefsDir() string {
	if env := os.Getenv("GUY_PREFS_DIR"); env != "" {
		return env
	}
	if runtime.GOOS == "windows" {
		base := os.Getenv("APPDATA")
		if base == "" {
			base = filepath.Join(homeDir(), "AppData", "Roaming")
		}
		return filepath.Join(base, AppName)
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, AppName)
	}
	return filepath.Join(homeDir(), ".config", AppName)
}

func DBPath() string        { return filepath.Join(DataDir(), "state.db") }
func LogsRoot() string      { return filepath.Join(DataDir(), "logs") }
func WorktreesRoot() string { return filepath.Join(DataDir(), "worktrees") }

func EnsureDirs() error {
	for _, p := range []string{DataDir(), PrefsDir(), LogsRoot(), WorktreesRoot()} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return "."
}

func PrefsPath() string { return filepath.Join(PrefsDir(), "prefs.json") }

type prefsFile struct {
	DefaultRepo string            `json:"default_repo"`
	Settings    map[string]any    `json:"settings"`
	Adapters    map[string]string `json:"adapters"`
}

func readPrefs() prefsFile {
	var p prefsFile
	data, err := os.ReadFile(PrefsPath())
	if err == nil {
		_ = json.Unmarshal(data, &p)
	}
	return p
}

// WritePrefsAtomic writes prefs via temp file + rename.
func writePrefsAtomic(p prefsFile) error {
	if err := os.MkdirAll(PrefsDir(), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	tmp := PrefsPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, PrefsPath())
}

func DefaultRepoRef() string { return readPrefs().DefaultRepo }

func SetDefaultRepo(ref string) error {
	p := readPrefs()
	p.DefaultRepo = ref
	return writePrefsAtomic(p)
}

func AdapterOverrides() map[string]string { return readPrefs().Adapters }

func SetAdapterOverride(name, path string) error {
	p := readPrefs()
	if p.Adapters == nil {
		p.Adapters = map[string]string{}
	}
	if path == "" {
		delete(p.Adapters, name)
	} else {
		p.Adapters[name] = path
	}
	return writePrefsAtomic(p)
}

func UserSettings() map[string]any {
	merged := map[string]any{}
	for k, v := range Defaults {
		merged[k] = v
	}
	for k, v := range readPrefs().Settings {
		merged[k] = v
	}
	return merged
}

func SetUserSetting(key string, value any) error {
	if _, ok := Defaults[key]; !ok {
		keys := make([]string, 0, len(Defaults))
		for k := range Defaults {
			keys = append(keys, k)
		}
		sortStrings(keys)
		return fmt.Errorf("unknown setting %q; known settings: %s", key, strings.Join(keys, ", "))
	}
	p := readPrefs()
	if p.Settings == nil {
		p.Settings = map[string]any{}
	}
	p.Settings[key] = value
	return writePrefsAtomic(p)
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// Setting helpers with typed accessors.
func SettingsInt(settings map[string]any, key string, fallback int) int {
	switch v := settings[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func SettingsString(settings map[string]any, key string, fallback string) string {
	if v, ok := settings[key].(string); ok {
		return v
	}
	return fallback
}

// RepoSettings reads optional per-repo config from .guy/project.json or the
// documented .guy/project.yaml subset.
func RepoSettings(repoPath string) (map[string]any, error) {
	guyDir := filepath.Join(repoPath, ".guy")
	if data, err := os.ReadFile(filepath.Join(guyDir, "project.json")); err == nil {
		var out map[string]any
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, fmt.Errorf("invalid project.json: %w", err)
		}
		return out, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if data, err := os.ReadFile(filepath.Join(guyDir, "project.yaml")); err == nil {
		return ParseYAMLSubset(string(data))
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return map[string]any{}, nil
}

// EffectiveSettings merges: CLI overrides > repo config > user prefs > defaults.
func EffectiveSettings(cliOverrides map[string]any, repoPath string) map[string]any {
	merged := UserSettings()
	if repoPath != "" {
		repoCfg, err := RepoSettings(repoPath)
		if err == nil {
			for key := range Defaults {
				if v, ok := repoCfg[key]; ok {
					merged[key] = v
				}
			}
		}
	}
	for k, v := range cliOverrides {
		if v != nil {
			merged[k] = v
		}
	}
	return merged
}

func parseScalar(tok string) any {
	tok = strings.TrimSpace(tok)
	if len(tok) >= 2 && tok[0] == tok[len(tok)-1] && (tok[0] == '"' || tok[0] == '\'') {
		return tok[1 : len(tok)-1]
	}
	switch strings.ToLower(tok) {
	case "true", "yes":
		return true
	case "false", "no":
		return false
	case "null", "~", "":
		return nil
	}
	if n, err := strconv.Atoi(tok); err == nil {
		return n
	}
	return tok
}

// ParseYAMLSubset parses the tiny documented YAML subset allowed in
// .guy/project.yaml: comments, "key: value", one nesting level, and "- item"
// lists. Anything else is an error; use project.json for anything richer.
func ParseYAMLSubset(text string) (map[string]any, error) {
	root := map[string]any{}
	type frame struct {
		indent    int
		container any // map[string]any or []any
		parent    map[string]any
		key       string
	}
	stack := []frame{{indent: -1, container: root}}
	for _, raw := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(raw) - len(strings.TrimLeft(raw, " "))
		for len(stack) > 1 && indent <= stack[len(stack)-1].indent {
			stack = stack[:len(stack)-1]
		}
		top := &stack[len(stack)-1]
		if strings.HasPrefix(trimmed, "- ") {
			value := parseScalar(trimmed[2:])
			switch c := top.container.(type) {
			case []any:
				top.container = append(c, value)
				if top.parent != nil {
					top.parent[top.key] = top.container // append may reallocate; write back
				}
			case map[string]any:
				if len(c) == 0 && top.parent != nil {
					list := []any{value}
					top.parent[top.key] = list
					top.container = list
				} else {
					return nil, fmt.Errorf("unsupported list item in .guy config: %q", raw)
				}
			default:
				return nil, fmt.Errorf("unsupported list item in .guy config: %q", raw)
			}
			continue
		}
		idx := strings.Index(trimmed, ":")
		if idx < 0 {
			return nil, fmt.Errorf("unsupported line in .guy config: %q (only 'key: value' and '- item' lines are supported; use project.json)", raw)
		}
		key := strings.TrimSpace(trimmed[:idx])
		rest := strings.TrimSpace(trimmed[idx+1:])
		if key == "" {
			return nil, fmt.Errorf("bad line in .guy config: %q", raw)
		}
		m, _ := top.container.(map[string]any)
		if m == nil {
			return nil, fmt.Errorf("bad nesting in .guy config: %q", raw)
		}
		if rest == "" {
			child := map[string]any{}
			m[key] = child
			stack = append(stack, frame{indent: indent, container: child, parent: m, key: key})
		} else {
			m[key] = parseScalar(rest)
		}
	}
	return root, nil
}
