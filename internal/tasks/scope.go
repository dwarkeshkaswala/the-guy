package tasks

import (
	"path/filepath"
	"strings"
)

// Concern prefixes owned by run-time checks. They are replaced (not
// duplicated) every time a task is re-run, so a retry that fixes the problem
// also clears the concern.
const (
	ConcernNoChanges  = "[no-changes] "
	ConcernOutOfScope = "[out-of-scope] "
)

// agentBookkeeping lists files an agent adapter itself writes into the
// worktree as part of its protocol; they are never the task's work.
var agentBookkeeping = map[string][]string{
	"mock": {"mock-prompt-received.txt", "mock-child.pid"},
}

// WorkFiles drops the adapter's own bookkeeping files from a changed-file
// list, leaving only files that represent the task's actual work.
func WorkFiles(agent string, changed []string) []string {
	ignore := map[string]bool{}
	for _, f := range agentBookkeeping[agent] {
		ignore[f] = true
	}
	var out []string
	for _, f := range changed {
		if !ignore[normalizeRel(f)] {
			out = append(out, f)
		}
	}
	return out
}

// OutOfScope returns the changed files not covered by allowed. An empty
// allowed list means "whole repository" (nothing is out of scope), matching
// the scheduler's collision semantics.
//
// A pattern covers a file when it is equal to it, is a directory prefix of it
// ("api" or "api/" covers "api/x.go"), or glob-matches it ('*' and '**' match
// any run of characters including '/', '?' matches one non-'/' character).
// Matching is deliberately lenient: this check flags agents that wandered,
// it is not a sandbox.
func OutOfScope(changed, allowed []string) []string {
	if len(allowed) == 0 {
		return nil
	}
	var out []string
	for _, f := range changed {
		if !InScope(f, allowed) {
			out = append(out, f)
		}
	}
	return out
}

// InScope reports whether one repo-relative path is covered by allowed.
func InScope(path string, allowed []string) bool {
	p := normalizeRel(path)
	for _, a := range allowed {
		pat := normalizeRel(a)
		if pat == "" || pat == "." {
			return true
		}
		if p == pat || strings.HasPrefix(p, pat+"/") {
			return true
		}
		if strings.ContainsAny(pat, "*?") && globAny(pat, p) {
			return true
		}
	}
	return false
}

func normalizeRel(p string) string {
	// Normalize separators on every OS (a plan written on Windows may say
	// "api\\" and run on Linux, or vice versa); git always reports '/'.
	p = strings.ReplaceAll(filepath.ToSlash(strings.TrimSpace(p)), "\\", "/")
	p = strings.TrimPrefix(p, "./")
	p = strings.TrimSuffix(p, "/")
	return p
}

// globAny matches pattern against s where '*' (and '**') match any sequence
// including '/', and '?' matches exactly one non-'/' character.
func globAny(pattern, s string) bool {
	// Iterative wildcard matching with single-star backtracking.
	pi, si := 0, 0
	star, mark := -1, 0
	for si < len(s) {
		switch {
		case pi < len(pattern) && pattern[pi] == '*':
			for pi < len(pattern) && pattern[pi] == '*' {
				pi++
			}
			star, mark = pi, si
		case pi < len(pattern) && (pattern[pi] == s[si] || (pattern[pi] == '?' && s[si] != '/')):
			pi++
			si++
		case star >= 0:
			mark++
			si = mark
			pi = star
		default:
			return false
		}
	}
	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}
	return pi == len(pattern)
}

// ReplaceConcerns drops concerns with the given prefix and appends fresh ones.
func ReplaceConcerns(existing []string, prefix string, fresh ...string) []string {
	out := make([]string, 0, len(existing)+len(fresh))
	for _, c := range existing {
		if !strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	for _, c := range fresh {
		out = append(out, prefix+c)
	}
	return out
}
