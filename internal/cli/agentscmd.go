package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dwkaswala/the-guy/internal/agents"
	"github.com/dwkaswala/the-guy/internal/config"
)

func cmdAgents(c *Ctx, p parsed) error {
	sub := p.arg(0)
	if sub == "" {
		sub = "list"
	}
	switch sub {
	case "list":
		return agentsList(c)
	case "doctor":
		return agentsDoctor(c, p.arg(1))
	case "set":
		return agentsSet(c, p.arg(1), p.arg(2))
	case "clear":
		return agentsClear(c, p.arg(1))
	case "rescan":
		return agentsRescan(c)
	}
	return usage("unknown agents subcommand %q (list|doctor|set|clear|rescan)", sub)
}

func agentsList(c *Ctx) error {
	reports := agents.ProbeAllCached()
	var lines []string
	lines = append(lines, "Adapters (capability-checked against the installed CLIs — never assumed):", "")
	for _, r := range reports {
		state := "not installed"
		if r.HeadlessVerified {
			state = "ready"
		} else if r.Installed {
			state = "installed, headless unverified"
		}
		where := ""
		if r.Executable != "" {
			where = " @ " + r.Executable
		}
		lines = append(lines, fmt.Sprintf("  %-10s %-24s [%s] %s%s", r.Adapter, r.Display, r.Kind, state, where))
		lines = append(lines, "             "+r.HeadlessNote)
		if r.Version != "" {
			lines = append(lines, "             version: "+r.Version)
		}
	}
	lines = append(lines, "",
		"Discovery: override (GUY_<NAME>_PATH / `guy agents set`) > PATH > adapter-specific roots > common install locations.",
		"Diagnostics: `guy agents doctor`; re-probe after installing: `guy agents rescan`.",
		"VS Code / Copilot is treated as the human review interface, not a headless worker.")
	return okErr(c, strings.Join(lines, "\n"), map[string]any{"adapters": reports})
}

func agentsDoctor(c *Ctx, name string) error {
	if name != "" {
		if _, err := agents.Get(name); err != nil {
			return err
		}
	}
	var selected []agents.CapabilityReport
	for _, r := range agents.ProbeAllCached() {
		if name == "" || r.Adapter == name {
			selected = append(selected, r)
		}
	}
	lines := []string{
		"Agent discovery diagnostics",
		"(read-only checks: --version / --help; nothing is installed or executed)",
		fmt.Sprintf("probe cache: %s", filepath.Join(config.DataDir(), "adapter-cache.json")),
		"",
	}
	for _, r := range selected {
		d := r.Discovery
		if d == nil {
			d = map[string]any{}
		}
		lines = append(lines, fmt.Sprintf("%s — %s [%s]", r.Adapter, r.Display, r.Kind))
		verdict := "DISABLED (no usable executable)"
		if r.HeadlessVerified {
			verdict = "READY"
		} else if r.Installed {
			verdict = "INSTALLED but headless UNVERIFIED — disabled"
		}
		lines = append(lines, "  verdict: "+verdict)
		if r.Executable != "" {
			source := ""
			if r.Source != "" {
				source = " (source: " + r.Source + ")"
			}
			lines = append(lines, "  executable: "+r.Executable+source)
		}
		if r.Version != "" {
			lines = append(lines, "  version: "+r.Version)
		}
		if r.HeadlessNote != "" {
			lines = append(lines, "  note: "+r.HeadlessNote)
		}
		if override, ok := d["override"].(string); ok && override != "" {
			lines = append(lines, fmt.Sprintf("  override: %v -> %s", d["override_source"], override))
		} else if envVar, ok := d["env_var"].(string); ok && envVar != "" {
			lines = append(lines, fmt.Sprintf("  override: none (env %s not set; no `guy agents set` on record)", envVar))
		}
		if !r.Installed {
			if wk, ok := d["well_known"].([]agents.WellKnownEntry); ok {
				lines = append(lines, "  common install locations:")
				for _, w := range wk {
					state := "absent"
					if w.Present {
						state = "present, no match"
					}
					if w.Hit != "" {
						state = "hit: " + w.Hit
					}
					label := ""
					if w.Label != "" {
						label = "[" + w.Label + "] "
					}
					lines = append(lines, fmt.Sprintf("    %s%s  —  %s", label, w.Root, state))
				}
			}
		}
		if scanned, ok := d["path_dirs_scanned"].(int); ok {
			lines = append(lines, fmt.Sprintf("  PATH directories scanned: %d", scanned))
		}
		if others, ok := d["hits"].([]agents.DiscoveryHit); ok {
			var seen []string
			for _, h := range others {
				if h.Path != r.Executable {
					seen = append(seen, fmt.Sprintf("%s (%s, %s)", h.Path, h.Source, h.Kind))
				}
			}
			if len(seen) > 0 {
				lines = append(lines, "  other candidates seen: "+strings.Join(seen, "; "))
			}
		}
		if r.ExtensionHint != "" {
			lines = append(lines, fmt.Sprintf("  VS Code: extension '%s' found — an extension is NOT a global CLI and does not enable this adapter", r.ExtensionHint))
		}
		if !r.HeadlessVerified && r.Kind == "real" {
			if a, err := agents.Get(r.Adapter); err == nil {
				lines = append(lines, "  to enable: "+a.InstallHint())
			}
		}
		lines = append(lines, "")
	}
	return okErr(c, strings.Join(lines, "\n"), map[string]any{"adapters": selected})
}

func agentsSet(c *Ctx, name, path string) error {
	if name == "" || path == "" {
		return usage("usage: guy agents set <adapter> <path>")
	}
	adapter, err := agents.Get(name)
	if err != nil {
		return err
	}
	if adapter.Kind() == "mock" {
		return fmt.Errorf("the built-in mock agent needs no executable override")
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("path does not exist: %s\nfind the launcher first (e.g. claude.cmd / claude.exe), then retry. Discovery also scans PATH and common install locations automatically", path)
	}
	if err := config.SetAdapterOverride(adapter.Name(), path); err != nil {
		return err
	}
	agents.ClearProbeCache()
	report := adapter.Probe(20e9)
	state := "NOT usable"
	if report.HeadlessVerified {
		state = "verified, adapter ready"
	} else if report.Installed {
		state = "FOUND but NOT verified — adapter stays disabled"
	}
	source := ""
	if report.Source != "" {
		source = " (source: " + report.Source + ")"
	}
	text := fmt.Sprintf("set %s -> %s\n  probe: %s\n  executable: %s%s\n  %s",
		adapter.Name(), path, state, report.Executable, source, report.HeadlessNote)
	return okErr(c, text, map[string]any{"adapter": adapter.Name(), "path": path, "report": report})
}

func agentsClear(c *Ctx, name string) error {
	if name == "" {
		return usage("usage: guy agents clear <adapter>")
	}
	adapter, err := agents.Get(name)
	if err != nil {
		return err
	}
	had := false
	if overrides := config.AdapterOverrides(); overrides[adapter.Name()] != "" {
		had = true
	}
	if err := config.SetAdapterOverride(adapter.Name(), ""); err != nil {
		return err
	}
	agents.ClearProbeCache()
	report := adapter.Probe(20e9)
	where := "no executable found (auto discovery)"
	if report.Executable != "" {
		where = "rediscovered: " + report.Executable
	}
	return okErr(c, fmt.Sprintf("cleared override for %s\n  %s", adapter.Name(), where),
		map[string]any{"adapter": adapter.Name(), "had_override": had, "report": report})
}

func agentsRescan(c *Ctx) error {
	agents.ClearProbeCache()
	reports := agents.ProbeAllCached()
	lines := []string{"adapter rescan (cache cleared, full discovery + read-only capability checks):"}
	for _, r := range reports {
		state := "not installed"
		if r.HeadlessVerified {
			state = "ready"
		} else if r.Installed {
			state = "installed, unverified"
		}
		where := ""
		if r.Executable != "" {
			where = " @ " + r.Executable
			if r.Source != "" {
				where += " (" + r.Source + ")"
			}
		}
		lines = append(lines, fmt.Sprintf("  %-10s %s%s", r.Adapter, state, where))
	}
	return okErr(c, strings.Join(lines, "\n"), map[string]any{"adapters": reports})
}
