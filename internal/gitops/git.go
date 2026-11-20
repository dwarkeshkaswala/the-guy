// Package gitops wraps the system Git CLI. Every call uses argument arrays —
// never interpolated shell strings. the-guy never pushes, never rebases user
// branches, never resets/cleans the user's working tree, and never writes to
// the default branch.
package gitops

import (
	"bytes"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

type GitError struct {
	Args   []string
	Code   int
	Detail string
}

func (e *GitError) Error() string {
	return fmt.Sprintf("git %s failed (exit %d): %s", strings.Join(e.Args, " "), e.Code, e.Detail)
}

type Result struct {
	Code   int
	Stdout string
	Stderr string
}

// Git runs git -C cwd with args. check=true turns a nonzero exit into *GitError.
func Git(cwd string, check bool, args ...string) (Result, error) {
	argv := append([]string{"git", "-C", cwd}, args...)
	cmd := exec.Command(argv[0], argv[1:]...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
		err = nil
	}
	res := Result{Code: code, Stdout: stdout.String(), Stderr: stderr.String()}
	if check && code != 0 {
		detail := strings.TrimSpace(res.Stderr)
		if detail == "" {
			detail = strings.TrimSpace(res.Stdout)
		}
		return res, &GitError{Args: args, Code: code, Detail: detail}
	}
	return res, err
}

var gitVersionCache *([3]int)

// Version parses `git version 2.45.1.windows.1` into a 3-tuple.
func Version() ([3]int, error) {
	if gitVersionCache != nil {
		return *gitVersionCache, nil
	}
	out, err := exec.Command("git", "--version").Output()
	if err != nil {
		return [3]int{}, fmt.Errorf("git not available: %w", err)
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) == 0 {
		return [3]int{}, fmt.Errorf("cannot parse git --version output")
	}
	nums := []int{}
	for _, part := range strings.Split(fields[len(fields)-1], ".") {
		n, err := strconv.Atoi(part)
		if err != nil {
			break
		}
		nums = append(nums, n)
	}
	for len(nums) < 3 {
		nums = append(nums, 0)
	}
	v := [3]int{nums[0], nums[1], nums[2]}
	gitVersionCache = &v
	return v, nil
}

func SupportsMergeTree() bool {
	v, err := Version()
	if err != nil {
		return false
	}
	return v[0] > 2 || (v[0] == 2 && v[1] >= 38)
}

func FindRoot(path string) (string, error) {
	res, err := Git(path, false, "rev-parse", "--show-toplevel")
	if err != nil || res.Code != 0 {
		return "", fmt.Errorf("not a git repository: %s", path)
	}
	root := strings.TrimSpace(res.Stdout)
	if root == "" {
		return "", fmt.Errorf("not a git repository: %s", path)
	}
	return root, nil
}

func CurrentBranch(root string) (string, error) {
	res, _ := Git(root, false, "symbolic-ref", "--short", "HEAD")
	if res.Code != 0 {
		return "", nil // detached HEAD
	}
	return strings.TrimSpace(res.Stdout), nil
}

func RevParse(root, ref string) (string, error) {
	res, err := Git(root, true, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(res.Stdout), nil
}

func BranchExists(root, name string) bool {
	res, _ := Git(root, false, "rev-parse", "--verify", "--quiet", "refs/heads/"+name)
	return res.Code == 0
}

// DefaultBranch detects the default branch; never assumes "main".
// Returns (branch, method).
func DefaultBranch(root string) (string, string) {
	if res, _ := Git(root, false, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); res.Code == 0 {
		name := strings.TrimSpace(res.Stdout)
		name = strings.TrimPrefix(name, "origin/")
		if name != "" {
			return name, "origin/HEAD"
		}
	}
	if res, _ := Git(root, false, "config", "--get", "init.defaultBranch"); res.Code == 0 {
		name := strings.TrimSpace(res.Stdout)
		if name != "" && BranchExists(root, name) {
			return name, "init.defaultBranch"
		}
	}
	if cur, _ := CurrentBranch(root); cur != "" && BranchExists(root, cur) {
		return cur, "current branch"
	}
	for _, candidate := range []string{"main", "master", "trunk"} {
		if BranchExists(root, candidate) {
			return candidate, fmt.Sprintf("existing branch '%s'", candidate)
		}
	}
	return "master", "git fallback"
}

// StatusPorcelain returns (xy, path) pairs; handles quoted paths.
func StatusPorcelain(root string) ([][2]string, error) {
	res, err := Git(root, true, "status", "--porcelain")
	if err != nil {
		return nil, err
	}
	var entries [][2]string
	for _, line := range strings.Split(res.Stdout, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(line) < 4 {
			continue
		}
		xy := line[:2]
		path := line[3:]
		if strings.HasPrefix(path, "\"") && strings.HasSuffix(path, "\"") && len(path) >= 2 {
			path = path[1 : len(path)-1]
		}
		entries = append(entries, [2]string{xy, path})
	}
	return entries, nil
}

func IsDirty(root string) bool {
	entries, err := StatusPorcelain(root)
	return err == nil && len(entries) > 0
}

func HeadCommit(root string) (string, error) { return RevParse(root, "HEAD") }

func WorktreeAdd(root, worktreePath, branch, baseCommit string) error {
	_, err := Git(root, true, "worktree", "add", "-b", branch, worktreePath, baseCommit)
	return err
}

func WorktreeAddExisting(root, worktreePath, branch string) error {
	_, err := Git(root, true, "worktree", "add", worktreePath, branch)
	return err
}

func WorktreeRemove(root, worktreePath string, force bool) error {
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, worktreePath)
	_, err := Git(root, true, args...)
	return err
}

type WorktreeInfo struct {
	Path     string
	Branch   string
	Head     string
	Detached bool
	Bare     bool
}

func WorktreeList(root string) ([]WorktreeInfo, error) {
	res, err := Git(root, true, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var out []WorktreeInfo
	current := WorktreeInfo{}
	flush := func() {
		if current.Path != "" {
			out = append(out, current)
		}
		current = WorktreeInfo{}
	}
	for _, line := range strings.Split(res.Stdout, "\n") {
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		key, value, _ := strings.Cut(line, " ")
		switch key {
		case "worktree":
			flush()
			current.Path = value
		case "branch":
			current.Branch = strings.TrimPrefix(value, "refs/heads/")
		case "HEAD":
			current.Head = value
		case "detached":
			current.Detached = true
		case "bare":
			current.Bare = true
		}
	}
	flush()
	return out, nil
}

func DiffStat(worktree, baseline string) (string, error) {
	res, err := Git(worktree, true, "diff", "--stat", baseline)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(res.Stdout), nil
}

func DiffNameOnly(worktree, baseline string) ([]string, error) {
	res, err := Git(worktree, true, "diff", "--name-only", baseline)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, l := range strings.Split(res.Stdout, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, strings.TrimSpace(l))
		}
	}
	return out, nil
}

// DiffPatch returns the unified diff of the worktree against the baseline
// commit, bounded to maxBytes (truncated=true when the full diff exceeded the
// bound). Untracked files appear as new-file diffs (via `git diff --no-index`,
// which never touches the index — no `git add -N` side effects).
func DiffPatch(worktree, baseline string, maxBytes int64) (string, bool, error) {
	res, err := Git(worktree, true, "diff", baseline)
	if err != nil {
		return "", false, err
	}
	patch := res.Stdout
	truncated := int64(len(patch)) > maxBytes
	if truncated {
		return patch[:maxBytes], true, nil
	}
	if untracked, err := Git(worktree, false, "ls-files", "--others", "--exclude-standard"); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(untracked.Stdout), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var newFile string
			if runtime.GOOS == "windows" {
				newFile = "NUL"
			} else {
				newFile = "/dev/null"
			}
			out, err := Git(worktree, false, "diff", "--no-index", "--", newFile, line)
			if err == nil || out.Code == 1 { // exit 1 means differences found
				patch += out.Stdout
				if int64(len(patch)) > maxBytes {
					return patch[:maxBytes], true, nil
				}
			}
		}
	}
	return patch, truncated, nil
}

func CommitsBetween(worktree, baseline, head string) ([]string, error) {
	res, err := Git(worktree, true, "log", "--oneline", baseline+".."+head)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, l := range strings.Split(res.Stdout, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, strings.TrimSpace(l))
		}
	}
	return out, nil
}

func CommitAll(worktree, message string) (string, error) {
	if _, err := Git(worktree, true, "add", "-A"); err != nil {
		return "", err
	}
	if _, err := Git(worktree, true, "commit", "-m", message); err != nil {
		return "", err
	}
	return HeadCommit(worktree)
}

func MergeBranch(worktree, branch, message string) error {
	_, err := Git(worktree, true, "merge", "--no-ff", "-m", message, branch)
	return err
}

func MergeAbort(worktree string) {
	_, _ = Git(worktree, false, "merge", "--abort")
}

func ConflictFiles(worktree string) []string {
	var files []string
	entries, _ := StatusPorcelain(worktree)
	for _, e := range entries {
		xy := e[0]
		if strings.Contains(xy, "U") || xy == "AA" || xy == "DD" {
			files = append(files, e[1])
		}
	}
	return files
}

// MergeTreeClean predicts merge cleanliness: true/false; nil when this git
// cannot do it (< 2.38).
func MergeTreeClean(root, oursRef, theirsRef string) (*bool, error) {
	if !SupportsMergeTree() {
		return nil, nil
	}
	res, _ := Git(root, false, "merge-tree", "--write-tree", oursRef, theirsRef)
	switch res.Code {
	case 0:
		clean := true
		return &clean, nil
	case 1:
		clean := false
		return &clean, nil
	default:
		detail := strings.TrimSpace(res.Stderr + res.Stdout)
		return nil, &GitError{Args: []string{"merge-tree"}, Code: res.Code, Detail: detail}
	}
}

func DeleteBranch(root, branch string, force bool) error {
	flag := "-d"
	if force {
		flag = "-D"
	}
	_, err := Git(root, true, "branch", flag, branch)
	return err
}
