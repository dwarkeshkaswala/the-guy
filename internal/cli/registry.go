package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dwkaswala/the-guy/internal/config"
	"github.com/dwkaswala/the-guy/internal/gitops"
	"github.com/dwkaswala/the-guy/internal/pathutil"
	"github.com/dwkaswala/the-guy/internal/store"
	"github.com/dwkaswala/the-guy/internal/tasks"
)

type registryError struct{ msg string }

func (e *registryError) Error() string { return e.msg }

var projectFileHints = []string{
	"package.json", "pyproject.toml", "requirements.txt", "setup.py", "Cargo.toml",
	"go.mod", "pom.xml", "build.gradle", "build.gradle.kts", "composer.json",
	"Gemfile", "mix.exs", "CMakeLists.txt", "Makefile", "deno.json",
}

var agentConfigHints = []string{
	"AGENTS.md", "CLAUDE.md", ".claude", ".github/copilot-instructions.md",
	".guy", ".codex", ".opencode",
}

func newRepoID() string {
	return "R-" + randomHex(8)
}

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := randRead(buf); err != nil {
		for i := range buf {
			buf[i] = byte(os.Getpid() + i)
		}
	}
	const digits = "0123456789abcdef"
	out := make([]byte, 0, n)
	for _, b := range buf {
		out = append(out, digits[b>>4], digits[b&0xf])
	}
	return string(out)
}

func registerRepo(st *store.Store, path, alias string) (*tasks.Repo, error) {
	if st, err := os.Stat(path); err != nil || !st.IsDir() {
		return nil, &registryError{msg: fmt.Sprintf("path does not exist or is not a directory: %s", path)}
	}
	root, err := gitops.FindRoot(path)
	if err != nil {
		return nil, err
	}
	if existing, _ := st.GetRepo(root); existing != nil {
		return nil, &registryError{msg: fmt.Sprintf(
			"repository already registered as '%s' (%s)", existing.Alias, existing.ID)}
	}
	if alias == "" {
		alias = filepath.Base(root)
	}
	if other, _ := st.GetRepo(alias); other != nil {
		return nil, &registryError{msg: fmt.Sprintf("alias '%s' is already in use", alias)}
	}
	defaultBranch, method := gitops.DefaultBranch(root)
	repo := &tasks.Repo{
		ID: newRepoID(), Path: root, Alias: alias,
		DefaultBranch: defaultBranch, AddedAt: tasks.UTCNow(),
	}
	if err := st.UpsertRepo(*repo); err != nil {
		return nil, err
	}
	_ = st.AppendEvent("repo_added", repo.ID, "", map[string]any{
		"path": repo.Path, "alias": alias, "default_branch": defaultBranch,
		"default_branch_method": method,
	})
	return repo, nil
}

func removeRepo(st *store.Store, ref string, purge bool) (*tasks.Repo, error) {
	repo, err := st.GetRepo(ref)
	if err != nil || repo == nil {
		return nil, &registryError{msg: "no registered repository matches '" + ref + "'"}
	}
	if purge {
		wtRoot := filepath.Join(config.WorktreesRoot(), repo.ID)
		if dirExists(wtRoot) {
			var dirty []string
			for _, wt := range listWorktrees(repo.Path) {
				if !pathutil.HasPrefix(wt.Path, wtRoot) {
					continue
				}
				if dirExists(wt.Path) && gitops.IsDirty(wt.Path) {
					dirty = append(dirty, wt.Path)
				}
			}
			if len(dirty) > 0 {
				return nil, &registryError{msg: "refusing to purge worktrees with uncommitted changes: " +
					strings.Join(dirty, "; ") + " — review them manually, then retry"}
			}
			for _, wt := range listWorktrees(repo.Path) {
				if dirExists(wt.Path) && pathutil.HasPrefix(wt.Path, wtRoot) {
					_ = gitops.WorktreeRemove(repo.Path, wt.Path, true)
				}
			}
		}
	}
	if err := st.DeleteRepo(repo.ID); err != nil {
		return nil, err
	}
	if config.DefaultRepoRef() == repo.Alias || config.DefaultRepoRef() == repo.ID {
		_ = config.SetDefaultRepo("")
	}
	_ = st.AppendEvent("repo_removed", "", "", map[string]any{
		"path": repo.Path, "alias": repo.Alias, "purged": purge})
	return repo, nil
}

func resolveRepo(st *store.Store, ref string) (*tasks.Repo, error) {
	if ref != "" {
		repo, err := st.GetRepo(ref)
		if err != nil {
			return nil, err
		}
		if repo == nil {
			return nil, &registryError{msg: "no registered repository matches '" + ref + "'"}
		}
		return repo, nil
	}
	if pref := config.DefaultRepoRef(); pref != "" {
		repo, _ := st.GetRepo(pref)
		if repo == nil {
			return nil, &registryError{msg: fmt.Sprintf(
				"default repo '%s' is no longer registered; run `guy repo list` and `guy use <alias>`", pref)}
		}
		return repo, nil
	}
	repos, _ := st.ListRepos()
	switch len(repos) {
	case 1:
		return &repos[0], nil
	case 0:
		return nil, &registryError{msg: "no repositories registered; run `guy repo add <path> --alias <name>`"}
	}
	names := make([]string, 0, len(repos))
	for _, r := range repos {
		names = append(names, r.Alias)
	}
	return nil, &registryError{msg: "multiple repositories registered; pass --repo <alias|path|id> or set a default with `guy use <alias>` (candidates: " + strings.Join(names, ", ") + ")"}
}

func listWorktrees(root string) []gitops.WorktreeInfo {
	wts, err := gitops.WorktreeList(root)
	if err != nil {
		return nil
	}
	return wts
}

func testHints(root string) []string {
	var hints []string
	if data, err := os.ReadFile(filepath.Join(root, "package.json")); err == nil {
		var pkg struct {
			Scripts map[string]string `json:"scripts"`
		}
		if json.Unmarshal(data, &pkg) == nil {
			if script, ok := pkg.Scripts["test"]; ok {
				hints = append(hints, "package.json scripts.test: "+script)
			}
		}
	}
	if data, err := os.ReadFile(filepath.Join(root, "pyproject.toml")); err == nil && strings.Contains(string(data), "pytest") {
		hints = append(hints, "pyproject.toml mentions pytest (e.g. python -m pytest)")
	}
	if data, err := os.ReadFile(filepath.Join(root, "Makefile")); err == nil && strings.Contains(string(data), "test") {
		hints = append(hints, "Makefile contains a test target")
	}
	return hints
}

func inspectRepo(st *store.Store, repo *tasks.Repo) map[string]any {
	root, err := gitops.FindRoot(repo.Path)
	if err != nil || root != repo.Path {
		return map[string]any{"repo": repo.Alias, "error": "registered path is no longer a git repository root: " + repo.Path}
	}
	entries, _ := gitops.StatusPorcelain(root)
	var dirtyFiles []string
	for _, e := range entries {
		dirtyFiles = append(dirtyFiles, e[1])
	}
	var projectFiles []string
	for _, f := range projectFileHints {
		if fileExists(filepath.Join(root, f)) {
			projectFiles = append(projectFiles, f)
		}
	}
	var agentConfigs []string
	for _, f := range agentConfigHints {
		if pathExists(filepath.Join(root, f)) {
			agentConfigs = append(agentConfigs, f)
		}
	}
	plans, _ := st.ListPlans(repo.ID)
	taskList, _ := st.ListTasks(repo.ID, "")
	statusCounts := map[string]int{}
	for _, t := range taskList {
		statusCounts[t.Status]++
	}
	wtRoot := filepath.Join(config.WorktreesRoot(), repo.ID)
	var managed []map[string]any
	for _, wt := range listWorktrees(root) {
		if pathutil.HasPrefix(wt.Path, wtRoot) {
			managed = append(managed, map[string]any{"path": wt.Path, "branch": wt.Branch})
		}
	}
	current, _ := gitops.CurrentBranch(root)
	head, _ := gitops.HeadCommit(root)
	if head == "" {
		head = "-"
	}
	dirtySample := dirtyFiles
	if len(dirtySample) > 10 {
		dirtySample = dirtySample[:10]
	}
	return map[string]any{
		"alias": repo.Alias, "id": repo.ID, "path": root,
		"current_branch": orUnknown(current), "default_branch": repo.DefaultBranch,
		"head_commit": head,
		"dirty":       len(dirtyFiles) > 0, "dirty_files": dirtySample,
		"dirty_file_count": len(dirtyFiles),
		"project_files":    projectFiles, "test_command_hints": testHints(root),
		"agent_config_files": agentConfigs,
		"plans_count":        len(plans), "task_status_counts": statusCounts,
		"managed_worktrees": managed,
	}
}

func orUnknown(s string) string {
	if s == "" {
		return "(detached)"
	}
	return s
}

func lower(s string) string { return strings.ToLower(s) }

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
