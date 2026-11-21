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
	"github.com/dwkaswala/the-guy/internal/review"
	"github.com/dwkaswala/the-guy/internal/scheduler"
)

// cmdDemo runs the full lifecycle on a disposable repository with the
// built-in mock agent: zero model cost, nothing pushed, the user's default
// branch untouched.
func cmdDemo(c *Ctx, p parsed) error {
	fmt.Fprintln(c.Out, "the-guy demo — disposable repository, mock agents, zero model cost, nothing pushed")
	demoBase := filepath.Join(config.DataDir(), "demo")
	if err := os.MkdirAll(demoBase, 0o755); err != nil {
		return err
	}
	repoPath := filepath.Join(demoBase, "demo repo") // path contains a space on purpose
	if existing, _ := c.Store.GetRepo("demo"); existing != nil {
		if sameDir(filepath.Dir(existing.Path), demoBase) {
			if _, err := removeRepo(c.Store, "demo", true); err != nil {
				return err
			}
		} else {
			return fmt.Errorf("alias 'demo' is used by one of your own repositories; remove or re-alias it before running the demo")
		}
	}
	if dirExists(repoPath) {
		removeAllLenient(repoPath)
	}
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		return err
	}

	step := func(n int, title, cmd string) {
		fmt.Fprintf(c.Out, "\n[%d] %s\n    $ %s\n", n, title, cmd)
	}

	step(1, "create disposable git repository (default branch 'trunk', not 'main')", "git init -b trunk")
	if _, err := gitops.Git(repoPath, true, "init", "-b", "trunk"); err != nil {
		return err
	}
	_, _ = gitops.Git(repoPath, true, "config", "user.email", "demo@the-guy.local")
	_, _ = gitops.Git(repoPath, true, "config", "user.name", "the-guy demo")
	_ = os.WriteFile(filepath.Join(repoPath, "README.md"), []byte("# the-guy demo repo\n"), 0o644)
	_ = os.WriteFile(filepath.Join(repoPath, ".gitignore"),
		[]byte("mock-prompt-received.txt\nmock-child.pid\n"), 0o644)
	_, _ = gitops.Git(repoPath, true, "add", "-A")
	if _, err := gitops.Git(repoPath, true, "commit", "-m", "init"); err != nil {
		return err
	}
	baselineHead, _ := gitops.HeadCommit(repoPath)

	step(2, "register the repo", fmt.Sprintf("guy repo add %q --alias demo", repoPath))
	repo, err := registerRepo(c.Store, repoPath, "demo")
	if err != nil {
		return err
	}
	fmt.Fprintf(c.Out, "    default branch detected: %s\n", repo.DefaultBranch)

	step(3, "create a draft two-task plan (mock agents, tiny deterministic tests)", "guy plan --file plan.json")
	self, _ := os.Executable()
	spec := map[string]any{
		"prompt": "Demo: two tiny features, second depends on the first",
		"tasks": []any{
			map[string]any{
				"key": "a", "title": "Write feature A",
				"objective":           "Create feature-a.txt containing 'alpha'.",
				"agent":               "mock",
				"acceptance_criteria": []string{"feature-a.txt contains 'alpha'"},
				"required_tests":      [][]string{{self, "mock-agent", "--check-file", "feature-a.txt", "alpha"}},
				"options": map[string]any{"mock": map[string]any{
					"write": []any{map[string]any{"path": "feature-a.txt", "content": "alpha"}}}},
				"timeout_seconds": 60,
			},
			map[string]any{
				"key": "b", "title": "Write feature B (depends on A)",
				"objective": "Create feature-b.txt containing 'beta'.",
				"agent":     "mock", "dependencies": []string{"a"},
				"acceptance_criteria": []string{"feature-b.txt contains 'beta'"},
				"required_tests":      [][]string{{self, "mock-agent", "--check-file", "feature-b.txt", "beta"}},
				"options": map[string]any{"mock": map[string]any{
					"write": []any{map[string]any{"path": "feature-b.txt", "content": "beta"}}}},
				"timeout_seconds": 60,
			},
		},
	}
	planFile := writePlanFileForDemo(demoBase, spec)
	// Round-trip through JSON so the spec has the same shape as a plan file.
	specData, err := os.ReadFile(planFile)
	if err != nil {
		return err
	}
	var roundTripped map[string]any
	if err := json.Unmarshal(specData, &roundTripped); err != nil {
		return err
	}
	plan, created, err := createPlanFromSpec(c.Store, repo, roundTripped, "", "mock")
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(created))
	for _, t := range created {
		ids = append(ids, fmt.Sprintf("%s(%s)", t.ID, t.Title))
	}
	fmt.Fprintf(c.Out, "    created %s: %s\n    (plan file: %s)\n", plan.ID, strings.Join(ids, ", "), planFile)

	step(4, "approve the plan (draft -> approved_for_execution)", "guy approve "+plan.ID)
	result, err := doApprove(c.Store, plan.ID, "")
	if err != nil {
		return err
	}
	fmt.Fprintf(c.Out, "    approved for execution: %s\n", strings.Join(toStringSlice(result["approved_for_execution"]), ", "))

	settings := config.EffectiveSettings(nil, repo.Path)
	sched := scheduler.New(c.Store, settings, repo)

	step(5, "dry-run (never launches anything)", "guy run --dry-run")
	selectable, err := selectRunTasks(c.Store, repo, "", nil)
	if err != nil {
		return err
	}
	for _, l := range sched.DryRun(selectable) {
		fmt.Fprintf(c.Out, "    would run %s (%s) in %s\n", l.TaskID, l.Agent, orRoot(l.Worktree))
	}

	step(6, "execute (task B is blocked until A is integrated)", "guy run --approve")
	sched.Execute(selectable, config.SettingsInt(settings, "max_workers", 2))
	taskA, _ := c.Store.GetTask(created[0].ID)
	taskB, _ := c.Store.GetTask(created[1].ID)
	fmt.Fprintf(c.Out, "    %s: %s\n", taskA.ID, taskA.Status)
	fmt.Fprintf(c.Out, "    %s: %s — %s\n", taskB.ID, taskB.Status, taskB.BlockedReason)

	step(7, "review A, run its tests, approve, integrate",
		fmt.Sprintf("guy review %s --run-tests && guy approve %s && guy integrate --approve", taskA.ID, taskA.ID))
	repoObj := repo
	if _, err := review.RunTests(c.Store, repoObj, taskA, settings); err != nil {
		return err
	}
	for _, r := range taskA.TestResults {
		fmt.Fprintf(c.Out, "    tests: %v %s\n", r.OK, r.Command)
	}
	if _, err := doApprove(c.Store, taskA.ID, ""); err != nil {
		return err
	}
	planTasks, _ := c.Store.ListTasks("", plan.ID)
	integ, err := review.PerformIntegration(c.Store, repoObj, plan, planTasks, false)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.Out, "    integrate: %v on %v\n", integ["status"], integ["integration_branch"])

	step(8, "now B is unblocked — execute, review, approve, integrate", "guy run --approve ...")
	sched2 := scheduler.New(c.Store, settings, repo)
	selectable2, err := selectRunTasks(c.Store, repo, "", []string{taskB.ID})
	if err != nil {
		return err
	}
	sched2.Execute(selectable2, config.SettingsInt(settings, "max_workers", 2))
	freshB, _ := c.Store.GetTask(taskB.ID)
	if _, err := review.RunTests(c.Store, repoObj, freshB, settings); err != nil {
		return err
	}
	fmt.Fprintf(c.Out, "    %s: %s; tests: %v\n", freshB.ID, freshB.Status, len(freshB.TestResults) > 0 && freshB.TestResults[0].OK)
	if _, err := doApprove(c.Store, freshB.ID, ""); err != nil {
		return err
	}
	planTasks2, _ := c.Store.ListTasks("", plan.ID)
	integ2, err := review.PerformIntegration(c.Store, repoObj, plan, planTasks2, false)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.Out, "    integrate: %v — integrated: %v\n", integ2["status"], strings.Join(toStringSlice(integ2["integrated"]), ", "))

	step(9, "verify safety invariants", "git -C <repo> log --oneline "+repo.DefaultBranch)
	finalHead, _ := gitops.HeadCommit(repoPath)
	safe := finalHead == baselineHead
	if res, err := gitops.Git(fmt.Sprint(integ2["integration_worktree"]), true, "log", "--oneline", "-5"); err == nil {
		for _, l := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
			if l != "" {
				fmt.Fprintln(c.Out, "    "+l)
			}
		}
	}
	fmt.Fprintf(c.Out, "    default branch '%s' unchanged: %v\n", repo.DefaultBranch, safe)
	fmt.Fprintln(c.Out, "    nothing pushed (the-guy never pushes); agent commits only via integrate --approve")

	if !p.bool("keep") {
		fmt.Fprintln(c.Out, "\n[cleanup] removing demo repo, worktrees, and registration")
		if _, err := removeRepo(c.Store, repo.Alias, true); err != nil {
			return err
		}
		removeAllLenient(repoPath)
		removeAllLenient(filepath.Join(config.WorktreesRoot(), repo.ID))
		fmt.Fprintln(c.Out, "    done (event history kept for audit; `guy events --limit 50` to view)")
	} else {
		fmt.Fprintf(c.Out, "\n[--keep] demo repo left at %s (alias '%s')\n", repoPath, repo.Alias)
	}
	fmt.Fprintln(c.Out, "\ndemo complete.")
	if !safe {
		return fmt.Errorf("safety invariant failed: default branch changed")
	}
	return nil
}

func orRoot(wt string) string {
	if wt == "" {
		return "repo root"
	}
	return wt
}

// removeAllLenient deletes a tree even when git left read-only object files
// (a Windows reality: os.RemoveAll fails on read-only files). Directories are
// chmod'ed 0o777 — 0o666 would strip the POSIX execute bit, making the tree
// untraversable and silently defeating both this removal and any later reuse
// of the path.
func removeAllLenient(path string) {
	_ = filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
		if err == nil && info != nil {
			if info.IsDir() {
				_ = os.Chmod(p, 0o777)
			} else {
				_ = os.Chmod(p, 0o666)
			}
		}
		return nil
	})
	_ = os.RemoveAll(path)
}

// sameDir compares two directory paths, tolerating symlinked roots (e.g.
// macOS TMPDIR: git resolves /var/folders to /private/var/folders).
func sameDir(a, b string) bool {
	if pathutil.Normalize(a) == pathutil.Normalize(b) {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}
