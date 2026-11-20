package agents

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Discovery precedence (explicit user intent wins; no silent fallback):
//  1. override — GUY_<NAME>_PATH env var, or persisted `guy agents set` config
//  2. PATH scan (directory order)
//  3. adapter-specific roots (Factory Droid app-* versioned installs)
//  4. well-known user install locations (npm global prefix, pnpm, yarn,
//     volta, scoop, cargo, ~/.local/bin, ~/bin, homebrew)
//
// Windows: .exe > .cmd > .bat > bare are launchable; .ps1 shims are discovered
// but never launched (CreateProcess cannot run them and execution policy may
// block them). A stale override disables the adapter with instructions.
// Everything derives from environment variables — no hardcoded versions,
// usernames, or absolute install directories. Nothing installs software or
// touches PATH.

const PS1Note = "only a PowerShell shim (.ps1) was found; the-guy does not launch .ps1 files directly (PowerShell execution policy may also block them). Use the .cmd/.exe shim in the same folder or reinstall the CLI so one exists."

type extRank struct {
	ext  string
	rank int
}

func extPriority() []extRank {
	if runtime.GOOS == "windows" {
		return []extRank{{".exe", 1}, {".cmd", 2}, {".bat", 3}, {"", 4}, {".ps1", 99}}
	}
	return []extRank{{"", 1}}
}

func supportedSuffix(ext string) bool {
	switch ext {
	case ".exe", ".cmd", ".bat", "":
		return true
	}
	return false
}

type discoveryHit struct {
	path   string
	source string
	rank   int
}

type LabeledRoot struct {
	Path  string
	Label string
}

type DiscoveryHit struct {
	Path   string `json:"path"`
	Source string `json:"source"`
	Kind   string `json:"kind"`
}

type WellKnownEntry struct {
	Root    string `json:"root"`
	Label   string `json:"label"`
	Present bool   `json:"present"`
	Hit     string `json:"hit"`
}

type DiscoveryResult struct {
	Names           []string
	EnvVarName      string
	Override        string
	OverrideSource  string // env | config
	OverrideError   string
	Chosen          string
	ChosenSource    string // env | config | PATH | well-known
	PS1Only         bool
	PS1Path         string
	PathDirsScanned int
	WellKnown       []WellKnownEntry
	Hits            []DiscoveryHit
}

// ResultToMap serializes a DiscoveryResult for reports/JSON.
func ResultToMap(r DiscoveryResult) map[string]any {
	return map[string]any{
		"names":             r.Names,
		"env_var":           r.EnvVarName,
		"override":          r.Override,
		"override_source":   r.OverrideSource,
		"override_error":    r.OverrideError,
		"chosen":            r.Chosen,
		"chosen_source":     r.ChosenSource,
		"ps1_only":          r.PS1Only,
		"ps1_path":          r.PS1Path,
		"path_dirs_scanned": r.PathDirsScanned,
		"well_known":        r.WellKnown,
		"hits":              r.Hits,
	}
}

func pathDirectories() []string {
	var out []string
	for _, part := range strings.Split(os.Getenv("PATH"), string(os.PathListSeparator)) {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

var (
	npmPrefixOnce sync.Once
	npmPrefixVal  string
	npmPrefixOK   bool
)

// NPMPrefix resolves the global npm prefix by asking npm itself (read-only:
// `npm prefix -g`, then `npm config get prefix`), falling back to npm's
// documented default. Cached per process.
func NPMPrefix() (string, bool) {
	npmPrefixOnce.Do(func() {
		names := []string{"npm"}
		if runtime.GOOS == "windows" {
			names = []string{"npm", "npm.cmd", "npm.exe"}
		}
		for _, name := range names {
			exe, err := exec.LookPath(name)
			if err != nil {
				continue
			}
			for _, args := range [][]string{{"prefix", "-g"}, {"config", "get", "prefix"}} {
				argv := append([]string{exe}, args...)
				if out, ok := runCapture(argv, 20*time.Second); ok {
					lines := nonEmptyLines(out)
					if len(lines) == 0 {
						continue
					}
					prefix := strings.Trim(lines[len(lines)-1], `"'`)
					if st, err := os.Stat(prefix); err == nil && st.IsDir() {
						npmPrefixVal, npmPrefixOK = prefix, true
						return
					}
				}
			}
		}
		if appdata := os.Getenv("APPDATA"); appdata != "" {
			defaultPrefix := filepath.Join(appdata, "npm")
			if st, err := os.Stat(defaultPrefix); err == nil && st.IsDir() {
				npmPrefixVal, npmPrefixOK = defaultPrefix, true
			}
		}
	})
	return npmPrefixVal, npmPrefixOK
}

// ResetNPMPrefixCache clears the process cache (tests).
func ResetNPMPrefixCache() {
	npmPrefixOnce = sync.Once{}
	npmPrefixVal, npmPrefixOK = "", false
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			out = append(out, t)
		}
	}
	return out
}

var factoryAppRe = regexp.MustCompile(`^app-(\d+(?:\.\d+)*)$`)

type labeledRootList = []LabeledRoot

// FactoryDroidRoots scans %LOCALAPPDATA%\Factory\app-*\resources\bin and
// returns them newest-first by NUMERIC version comparison (1.10.0 beats
// 1.9.0, never alphabetical). Entries without resources\bin are skipped so a
// broken newest install falls through to the next valid one.
func FactoryDroidRoots() labeledRootList {
	var out labeledRootList
	if runtime.GOOS != "windows" {
		return out
	}
	local := os.Getenv("LOCALAPPDATA")
	if local == "" {
		return out
	}
	base := filepath.Join(local, "Factory")
	items, err := os.ReadDir(base)
	if err != nil {
		return out
	}
	type versioned struct {
		nums []int
		name string
		dir  string
	}
	var candidates []versioned
	for _, item := range items {
		m := factoryAppRe.FindStringSubmatch(item.Name())
		if m == nil || !item.IsDir() {
			continue
		}
		var nums []int
		for _, part := range strings.Split(m[1], ".") {
			n, err := strconv.Atoi(part)
			if err != nil {
				nums = nil
				break
			}
			nums = append(nums, n)
		}
		if nums == nil {
			continue
		}
		candidates = append(candidates, versioned{nums: nums, name: item.Name(),
			dir: filepath.Join(base, item.Name())})
	}
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		n := len(a.nums)
		if len(b.nums) > n {
			n = len(b.nums)
		}
		for k := 0; k < n; k++ {
			av, bv := 0, 0
			if k < len(a.nums) {
				av = a.nums[k]
			}
			if k < len(b.nums) {
				bv = b.nums[k]
			}
			if av != bv {
				return av > bv
			}
		}
		if len(a.nums) != len(b.nums) {
			return len(a.nums) > len(b.nums)
		}
		return a.name > b.name
	})
	for _, c := range candidates {
		binDir := filepath.Join(c.dir, "resources", "bin")
		if st, err := os.Stat(binDir); err == nil && st.IsDir() {
			out = append(out, LabeledRoot{Path: binDir, Label: "Factory Droid install (" + c.name + ")"})
		}
	}
	return out
}

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	return "."
}

// LabeledWellKnownRoots returns common user-level install locations with
// diagnostic labels, deduplicated, in priority order.
func LabeledWellKnownRoots() labeledRootList {
	var entries labeledRootList
	if prefix, ok := NPMPrefix(); ok {
		entries = append(entries, LabeledRoot{prefix, "npm global prefix"})
	}
	home := homeDir()
	if runtime.GOOS == "windows" {
		if appdata := os.Getenv("APPDATA"); appdata != "" {
			defaultNPM := filepath.Join(appdata, "npm")
			if len(entries) == 0 || !samePath(defaultNPM, entries[0].Path) {
				entries = append(entries, LabeledRoot{defaultNPM, `npm default prefix (%APPDATA%\npm)`})
			}
		}
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			entries = append(entries,
				LabeledRoot{filepath.Join(local, "pnpm"), "pnpm global"},
				LabeledRoot{filepath.Join(local, "Yarn", "bin"), "yarn global"},
				LabeledRoot{filepath.Join(local, "Volta", "bin"), "volta"})
		}
		entries = append(entries,
			LabeledRoot{filepath.Join(home, ".volta", "bin"), "volta (home)"},
			LabeledRoot{filepath.Join(home, "scoop", "shims"), "scoop shims"},
			LabeledRoot{filepath.Join(home, ".cargo", "bin"), "cargo"},
			LabeledRoot{filepath.Join(home, ".local", "bin"), "user local bin"},
			LabeledRoot{filepath.Join(home, "bin"), "personal bin"})
	} else {
		entries = append(entries,
			LabeledRoot{filepath.Join(home, ".local", "bin"), "user local bin"},
			LabeledRoot{filepath.Join(home, "bin"), "personal bin"},
			LabeledRoot{filepath.Join(home, ".volta", "bin"), "volta"},
			LabeledRoot{filepath.Join(home, ".cargo", "bin"), "cargo"},
			LabeledRoot{"/usr/local/bin", ""},
			LabeledRoot{"/opt/homebrew/bin", "homebrew"},
			LabeledRoot{"/opt/local/bin", "macports"})
	}
	seen := map[string]bool{}
	var out labeledRootList
	for _, e := range entries {
		key := lowerPath(e.Path)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, e)
	}
	return out
}

func WellKnownRoots() []string {
	var out []string
	for _, r := range LabeledWellKnownRoots() {
		out = append(out, r.Path)
	}
	return out
}

func adapterRoots(names []string) labeledRootList {
	primary := ""
	if len(names) > 0 {
		primary = strings.ToLower(names[0])
	}
	if primary == "droid" {
		return FactoryDroidRoots()
	}
	return nil
}

var vscodeKeywords = map[string][]string{
	"claude":   {"claude"},
	"opencode": {"opencode"},
	"codex":    {"codex", "chatgpt"},
	"droid":    {"droid", "factory"},
}

// VSCodeExtensionDir honors GUY_VSCODE_EXTENSIONS_DIR (tests), else
// ~/.vscode/extensions. An extension is only ever a HINT — never a CLI.
func VSCodeExtensionDir() string {
	if env := os.Getenv("GUY_VSCODE_EXTENSIONS_DIR"); env != "" {
		return env
	}
	return filepath.Join(homeDir(), ".vscode", "extensions")
}

func VSCodeExtensionHit(adapterName string) string {
	base := VSCodeExtensionDir()
	items, err := os.ReadDir(base)
	if err != nil {
		return ""
	}
	keywords := vscodeKeywords[adapterName]
	if keywords == nil {
		keywords = []string{adapterName}
	}
	var names []string
	for _, item := range items {
		names = append(names, item.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		low := strings.ToLower(name)
		for _, k := range keywords {
			if strings.Contains(low, k) {
				return name
			}
		}
	}
	return ""
}

func kindOf(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	return strings.TrimPrefix(ext, ".")
}

func samePath(a, b string) bool { return lowerPath(a) == lowerPath(b) }

func lowerPath(p string) string {
	if runtime.GOOS == "windows" {
		return strings.ToLower(filepath.Clean(p))
	}
	return filepath.Clean(p)
}

// Resolve finds the best executable for an adapter. Read-only; never launches it.
func Resolve(names []string, envVar, configPath string) DiscoveryResult {
	primary := names[0]
	result := DiscoveryResult{Names: names, EnvVarName: envVar,
		WellKnown: []WellKnownEntry{}, Hits: []DiscoveryHit{}}
	var allHits []discoveryHit
	seenPaths := map[string]bool{}

	record := func(dir, source string) {
		for _, name := range names {
			for _, er := range extPriority() {
				p := filepath.Join(dir, name+er.ext)
				if st, err := os.Stat(p); err == nil && !st.IsDir() {
					key := lowerPath(p)
					if seenPaths[key] {
						continue
					}
					seenPaths[key] = true
					allHits = append(allHits, discoveryHit{p, source, er.rank})
				}
			}
		}
	}

	envValue := ""
	if envVar != "" {
		envValue = os.Getenv(envVar)
	}
	override := envValue
	overrideSource := ""
	if override != "" {
		overrideSource = "env"
	} else if configPath != "" {
		override = configPath
		overrideSource = "config"
	}

	scan := func() {
		seenDirs := map[string]bool{}
		for _, dir := range pathDirectories() {
			key := lowerPath(dir)
			if seenDirs[key] {
				continue
			}
			seenDirs[key] = true
			record(dir, "PATH")
			result.PathDirsScanned++
		}
		roots := append(adapterRoots(names), LabeledWellKnownRoots()...)
		for _, root := range roots {
			present := dirExists(root.Path)
			before := len(allHits)
			if present {
				record(root.Path, "well-known")
			}
			entry := WellKnownEntry{Root: root.Path, Label: root.Label, Present: present}
			if present && len(allHits) > before {
				entry.Hit = allHits[len(allHits)-1].path
			}
			result.WellKnown = append(result.WellKnown, entry)
		}
	}

	if override != "" {
		result.Override = override
		result.OverrideSource = overrideSource
		p := expandHome(override)
		var variants []discoveryHit
		if dirExists(p) {
			for _, er := range extPriority() {
				q := filepath.Join(p, primary+er.ext)
				if fileExists(q) {
					variants = append(variants, discoveryHit{q, overrideSource, er.rank})
				}
			}
			if len(variants) == 0 {
				result.OverrideError = fmt.Sprintf(
					"configured path is a directory with no '%s' executable in it: %s", primary, p)
			}
		} else if fileExists(p) {
			ext := strings.ToLower(filepath.Ext(p))
			if supportedSuffix(ext) {
				rank := 4
				for _, er := range extPriority() {
					if er.ext == ext {
						rank = er.rank
					}
				}
				variants = append(variants, discoveryHit{p, overrideSource, rank})
			} else {
				result.OverrideError = fmt.Sprintf(
					"configured path is a '%s' file the-guy cannot launch directly: %s (use the .exe/.cmd launcher; .ps1 cannot be launched and may be blocked by PowerShell execution policy)",
					extOrPlain(ext), p)
			}
		} else {
			result.OverrideError = fmt.Sprintf(
				"configured executable path no longer exists: %s (set via %s; fix with `guy agents set %s <path>` or clear it with `guy agents clear %s`)",
				p, overrideSource, primary, primary)
		}
		if len(variants) > 0 {
			best := variants[0]
			for _, v := range variants[1:] {
				if v.rank < best.rank {
					best = v
				}
			}
			if best.rank >= 99 {
				result.PS1Only, result.PS1Path = true, best.path
				result.OverrideError = PS1Note
			} else {
				result.Chosen, result.ChosenSource = best.path, best.source
			}
		} else {
			scan() // informational only; Chosen stays empty — no fallback
		}
		finalize(&result, allHits)
		return result
	}

	scan()
	finalize(&result, allHits)
	return result
}

func extOrPlain(ext string) string {
	if ext == "" {
		return "plain"
	}
	return ext
}

func finalize(result *DiscoveryResult, allHits []discoveryHit) {
	for _, h := range allHits {
		result.Hits = append(result.Hits, DiscoveryHit{Path: h.path, Source: h.source, Kind: kindOf(h.path)})
	}
	sourcePriority := map[string]int{"env": 0, "config": 0, "PATH": 1, "well-known": 2}
	bestIdx := -1
	for i, h := range allHits {
		if h.rank >= 99 {
			continue
		}
		if bestIdx < 0 {
			bestIdx = i
			continue
		}
		cur := allHits[bestIdx]
		if sourcePriority[h.source] < sourcePriority[cur.source] ||
			(sourcePriority[h.source] == sourcePriority[cur.source] && h.rank < cur.rank) {
			bestIdx = i
		}
	}
	if bestIdx >= 0 {
		result.Chosen = allHits[bestIdx].path
		result.ChosenSource = allHits[bestIdx].source
		return
	}
	for _, h := range allHits {
		if h.rank >= 99 {
			result.PS1Only, result.PS1Path = true, h.path
			break
		}
	}
}

func expandHome(p string) string {
	if len(p) >= 2 && (p[:2] == "~/" || p[:2] == `~\`) {
		return filepath.Join(homeDir(), p[2:])
	}
	return p
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
