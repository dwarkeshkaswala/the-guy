package agents

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/dwkaswala/the-guy/internal/config"
)

// Probe cache: display-only, with automatic rediscovery. The fingerprint
// covers PATH, PATHEXT, env/config overrides, the adapter set, well-known
// roots, Factory app-* dirs (upgrade detection), the npm prefix, the VS Code
// dir and the binary version; a 300s TTL catches installs that changed no
// environment setting; cached "installed" reports are re-validated against
// the filesystem. Execution paths always probe live. `guy agents rescan`
// clears the cache explicitly.

const cacheTTL = 300 * time.Second

func cachePath() string { return filepath.Join(config.DataDir(), "adapter-cache.json") }

var buildVersion = "dev"

// SetBuildVersion records the binary version for fingerprinting (called from main).
func SetBuildVersion(v string) {
	buildVersion = v
	ClearProbeCache()
}

func Fingerprint() string {
	envOverrides := map[string]string{}
	for _, a := range All() {
		envOverrides[a.Name()] = os.Getenv(a.EnvVar())
	}
	factoryRoots := []string{}
	for _, r := range FactoryDroidRoots() {
		factoryRoots = append(factoryRoots, r.Path)
	}
	prefix, _ := NPMPrefix()
	parts := map[string]any{
		"version":          buildVersion,
		"path":             os.Getenv("PATH"),
		"pathext":          os.Getenv("PATHEXT"),
		"adapter_names":    adapterNames(),
		"env_overrides":    envOverrides,
		"config_overrides": config.AdapterOverrides(),
		"well_known_roots": WellKnownRoots(),
		"factory_droid":    factoryRoots,
		"npm_prefix":       prefix,
		"vscode_dir":       VSCodeExtensionDir(),
	}
	data, _ := json.Marshal(parts)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func adapterNames() []string {
	var names []string
	for _, a := range All() {
		names = append(names, a.Name())
	}
	sort.Strings(names)
	return names
}

func ClearProbeCache() {
	_ = os.Remove(cachePath())
}

type cacheFile struct {
	Fingerprint     string             `json:"fingerprint"`
	GeneratedAt     string             `json:"generated_at"`
	GeneratedAtUnix float64            `json:"generated_at_epoch"`
	Reports         []CapabilityReport `json:"reports"`
}

func ProbeAll() []CapabilityReport {
	reports := make([]CapabilityReport, 0)
	for _, a := range All() {
		reports = append(reports, a.Probe(20*time.Second))
	}
	return reports
}

// ProbeAllCached returns cached reports when the fingerprint matches, the
// cache is fresh, and every cached "installed" executable still exists;
// otherwise it re-probes and rewrites the cache.
func ProbeAllCached() []CapabilityReport {
	path := cachePath()
	fingerprint := Fingerprint()
	var cached cacheFile
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &cached)
	}
	if cached.Fingerprint == fingerprint && time.Since(time.Unix(int64(cached.GeneratedAtUnix), 0)) < cacheTTL {
		if len(cached.Reports) == len(All()) {
			wanted := map[string]bool{}
			for _, a := range All() {
				wanted[a.Name()] = true
			}
			stale := false
			out := make([]CapabilityReport, 0, len(cached.Reports))
			for i := range cached.Reports {
				r := cached.Reports[i]
				if !wanted[r.Adapter] {
					stale = true
					break
				}
				if r.Installed && r.Executable != "" && !fileExists(r.Executable) {
					stale = true
					break
				}
				out = append(out, r)
			}
			if !stale {
				return out
			}
		}
	}
	live := ProbeAll()
	now := time.Now()
	cf := cacheFile{
		Fingerprint:     fingerprint,
		GeneratedAt:     now.Format(time.RFC3339),
		GeneratedAtUnix: float64(now.UnixNano()) / 1e9,
		Reports:         live,
	}
	if data, err := json.MarshalIndent(cf, "", "  "); err == nil {
		tmp := path + ".tmp"
		if os.WriteFile(tmp, data, 0o644) == nil {
			_ = os.Rename(tmp, path)
		}
	}
	return live
}
