package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/dwkaswala/the-guy/internal/config"
	"github.com/dwkaswala/the-guy/internal/store"
)

// cmdMigrate adopts state created by the Python implementation. The Go and
// Python CLIs share the exact same SQLite schema and prefs.json format, so
// migration is NON-DESTRUCTIVE by design:
//
//   - --check only verifies and reports what would be adopted.
//   - migration first writes a timestamped backup copy of the source state.db
//     (and of any pre-existing target db) before anything is copied.
//   - rollback = restore the backup file (see docs/go-migration.md); the
//     Python CLI keeps working against the same state either way.
func cmdMigrate(c *Ctx, p parsed) error {
	sourceDir := p.flag("from")
	if sourceDir == "" {
		// The Python implementation used the same OS-appropriate defaults
		// (%LOCALAPPDATA%\the-guy on Windows) — resolve it independently of
		// GUY_DATA_DIR so an explicit --from is only needed for custom setups.
		sourceDir = pythonDefaultDataDir()
	}
	sourceDB := filepath.Join(sourceDir, "state.db")
	targetDB := config.DBPath()
	if _, err := os.Stat(sourceDB); err != nil {
		return fmt.Errorf("no Python state found at %s (set --from <dir> if it lives elsewhere)", sourceDB)
	}

	// Verify the source schema by opening it read-only-ish (open + check tables).
	src, err := store.Open(sourceDB)
	if err != nil {
		return fmt.Errorf("cannot open Python state db %s: %v", sourceDB, err)
	}
	if err := src.SchemaCompatible(); err != nil {
		_ = src.Close()
		return fmt.Errorf("Python state is not compatible: %v (this tool expects the schema from the-guy Python 0.1.x)", err)
	}
	repos, _ := src.ListRepos()
	plans, _ := src.ListPlans("")
	allTasks := 0
	for _, pl := range plans {
		ts, _ := src.ListTasks("", pl.ID)
		allTasks += len(ts)
	}
	approvals, _ := src.ListApprovals("")
	_ = src.Close()

	summary := func() string {
		return fmt.Sprintf("Python state at %s\n  repos: %d  plans: %d  tasks: %d  approvals: %d",
			sourceDB, len(repos), len(plans), allTasks, len(approvals))
	}
	if p.bool("check") {
		text := "migrate --check (read-only)\n" + summary() +
			fmt.Sprintf("\n  target: %s\n  schema: compatible (same tables; Go and Python share it)", targetDB)
		return okErr(c, text, map[string]any{
			"source": sourceDB, "target": targetDB, "compatible": true,
			"repos": len(repos), "plans": len(plans), "tasks": allTasks, "approvals": len(approvals)})
	}

	stamp := time.Now().Format("20060102-150405")
	backup := func(path string) (string, error) {
		if _, err := os.Stat(path); err != nil {
			return "", nil
		}
		out := fmt.Sprintf("%s.pre-go-backup-%s", path, stamp)
		if err := copyFile(path, out); err != nil {
			return "", err
		}
		return out, nil
	}
	sourceBackup, err := backup(sourceDB)
	if err != nil {
		return fmt.Errorf("failed to back up source db: %v", err)
	}
	targetBackup, err := backup(targetDB)
	if err != nil {
		return fmt.Errorf("failed to back up existing target db: %v", err)
	}
	if filepath.Clean(sourceDB) != filepath.Clean(targetDB) {
		if err := copyFile(sourceDB, targetDB); err != nil {
			return fmt.Errorf("failed to copy state into %s: %v", targetDB, err)
		}
		// WAL sidecars of the old target are stale now.
		for _, ext := range []string{"-wal", "-shm"} {
			_ = os.Remove(targetDB + ext)
		}
	}
	adopted, err := store.Open(targetDB)
	if err != nil {
		return fmt.Errorf("adopted state failed to open: %v (rollback: restore %s)", err, sourceBackup)
	}
	if err := adopted.SchemaCompatible(); err != nil {
		_ = adopted.Close()
		return fmt.Errorf("adopted state is not compatible: %v (rollback: restore %s)", err, sourceBackup)
	}
	_ = adopted.Close()
	_ = c.Store.AppendEvent("state_migrated_from_python", "", "", map[string]any{
		"source": sourceDB, "repos": len(repos), "plans": len(plans), "tasks": allTasks})

	text := "migration complete (non-destructive; Python CLI also keeps working on this state)\n" +
		summary() +
		fmt.Sprintf("\n  target: %s", targetDB)
	if sourceBackup != "" {
		text += "\n  backup of source: " + sourceBackup
	}
	if targetBackup != "" {
		text += "\n  backup of previous target: " + targetBackup
	}
	text += "\n  rollback: copy a backup file back over " + targetDB + " (see docs/go-migration.md)"
	return okErr(c, text, map[string]any{
		"source": sourceDB, "target": targetDB, "source_backup": sourceBackup,
		"target_backup": targetBackup, "repos": len(repos), "plans": len(plans),
		"tasks": allTasks})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return nil
}

// pythonDefaultDataDir computes the Python implementation's default data dir
// without consulting GUY_DATA_DIR (so migration can find it even when the Go
// CLI is redirected elsewhere for testing).
func pythonDefaultDataDir() string {
	dir := os.Getenv("LOCALAPPDATA")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, "AppData", "Local")
	}
	return filepath.Join(dir, "the-guy")
}
