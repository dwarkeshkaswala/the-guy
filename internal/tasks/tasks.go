// Package tasks defines the core data model: repos, plans, tasks, runs,
// approvals; the task lifecycle state machine; and dependency-DAG validation.
// Status strings match the Python implementation exactly so both CLIs can
// share one SQLite state file.
package tasks

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

func UTCNow() string { return time.Now().UTC().Format(time.RFC3339) }

const (
	StatusDraft                = "draft"
	StatusApprovedForExecution = "approved_for_execution"
	StatusQueued               = "queued"
	StatusRunning              = "running"
	StatusNeedsReview          = "needs_review"
	StatusApproved             = "approved"
	StatusIntegrated           = "integrated"
	StatusBlocked              = "blocked"
	StatusFailed               = "failed"
	StatusCancelled            = "cancelled"
)

var Terminal = map[string]bool{StatusIntegrated: true, StatusCancelled: true}

// Transitions is the deterministic state machine. `approve` is never implied
// by a process exit code.
var Transitions = map[string]map[string]bool{
	StatusDraft:                {StatusApprovedForExecution: true, StatusBlocked: true, StatusCancelled: true},
	StatusApprovedForExecution: {StatusQueued: true, StatusBlocked: true, StatusFailed: true, StatusCancelled: true},
	StatusQueued:               {StatusRunning: true, StatusBlocked: true, StatusFailed: true, StatusCancelled: true},
	StatusRunning:              {StatusNeedsReview: true, StatusFailed: true, StatusCancelled: true},
	StatusNeedsReview:          {StatusApproved: true, StatusFailed: true, StatusCancelled: true},
	StatusApproved:             {StatusIntegrated: true, StatusCancelled: true},
	StatusFailed:               {StatusQueued: true, StatusCancelled: true},
	StatusBlocked:              {StatusQueued: true, StatusApprovedForExecution: true, StatusDraft: true, StatusFailed: true, StatusCancelled: true},
	StatusIntegrated:           {},
	StatusCancelled:            {},
}

type TransitionError struct{ Msg string }

func (e *TransitionError) Error() string { return e.Msg }

func TransitionAllowed(current, next string) bool {
	allowed, ok := Transitions[current]
	if !ok {
		return false
	}
	return allowed[next]
}

type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

type Repo struct {
	ID            string `json:"id"`
	Path          string `json:"path"`
	Alias         string `json:"alias"`
	DefaultBranch string `json:"default_branch"`
	AddedAt       string `json:"added_at"`
}

type Plan struct {
	ID             string `json:"id"`
	RepoID         string `json:"repo_id"`
	Prompt         string `json:"prompt"`
	BaselineBranch string `json:"baseline_branch"`
	BaselineCommit string `json:"baseline_commit"`
	Status         string `json:"status"`
	CreatedAt      string `json:"created_at"`
}

// Task mirrors the Python task row (JSON-encoded list/map columns).
type Task struct {
	ID                 string         `json:"id"`
	PlanID             string         `json:"plan_id"`
	RepoID             string         `json:"repo_id"`
	Title              string         `json:"title"`
	Objective          string         `json:"objective"`
	Agent              string         `json:"agent"`
	Dependencies       []string       `json:"dependencies"`
	ReadOnly           bool           `json:"read_only"`
	AllowedPaths       []string       `json:"allowed_paths"`
	AcceptanceCriteria []string       `json:"acceptance_criteria"`
	RequiredTests      [][]string     `json:"required_tests"`
	TimeoutSeconds     *int           `json:"timeout_seconds"`
	MaxRetries         int            `json:"max_retries"`
	RetryCount         int            `json:"retry_count"`
	Status             string         `json:"status"`
	BlockedReason      string         `json:"blocked_reason"`
	BaselineBranch     string         `json:"baseline_branch"`
	BaselineCommit     string         `json:"baseline_commit"`
	Worktree           string         `json:"worktree"`
	Branch             string         `json:"branch"`
	CreatedAt          string         `json:"created_at"`
	StartedAt          string         `json:"started_at"`
	FinishedAt         string         `json:"finished_at"`
	ExitCode           *int           `json:"exit_code"`
	ExitReason         string         `json:"exit_reason"`
	ChangedFiles       []string       `json:"changed_files"`
	Commits            []string       `json:"commits"`
	TestResults        []TestResult   `json:"test_results"`
	Concerns           []string       `json:"concerns"`
	Options            map[string]any `json:"options"`
	Handoff            map[string]any `json:"handoff"`
}

type TestResult struct {
	Command      string `json:"command"`
	OK           bool   `json:"ok"`
	ExitCode     *int   `json:"exit_code"`
	FailureClass string `json:"failure_class,omitempty"`
	DurationMS   int    `json:"duration_ms"`
	LogPath      string `json:"log_path,omitempty"`
	Error        string `json:"error,omitempty"`
}

// ApplyTransition validates and applies a state change (idempotent for the
// same status). Sets/clears BlockedReason for blocked transitions.
func (t *Task) ApplyTransition(next string, reason string) error {
	if t.Status == next {
		return nil
	}
	if !TransitionAllowed(t.Status, next) {
		msg := fmt.Sprintf("illegal transition for %s: %s -> %s", t.ID, t.Status, next)
		if reason != "" {
			msg += " (" + reason + ")"
		}
		return &TransitionError{Msg: msg}
	}
	t.Status = next
	if next == StatusBlocked {
		if reason == "" {
			reason = "blocked"
		}
		t.BlockedReason = reason
	} else if t.BlockedReason != "" {
		t.BlockedReason = ""
	}
	return nil
}

type RunRecord struct {
	ID           string   `json:"id"`
	TaskID       string   `json:"task_id"`
	Adapter      string   `json:"adapter"`
	Argv         []string `json:"argv"`
	Cwd          string   `json:"cwd"`
	Pid          int      `json:"pid"`
	OwnerPid     int      `json:"owner_pid"`
	Status       string   `json:"status"`
	ExitCode     *int     `json:"exit_code"`
	FailureClass string   `json:"failure_class"`
	StartedAt    string   `json:"started_at"`
	FinishedAt   string   `json:"finished_at"`
	DurationMS   int      `json:"duration_ms"`
	LogPath      string   `json:"log_path"`
}

type ApprovalRecord struct {
	ID         string `json:"id"`
	TargetKind string `json:"target_kind"`
	TargetID   string `json:"target_id"`
	Kind       string `json:"kind"`
	Note       string `json:"note"`
	CreatedAt  string `json:"created_at"`
}

type EventRecord struct {
	Seq     int            `json:"seq"`
	TS      string         `json:"ts"`
	Kind    string         `json:"kind"`
	RepoID  string         `json:"repo_id"`
	TaskID  string         `json:"task_id"`
	Payload map[string]any `json:"payload"`
}

// TaskSpec is a normalized task definition from a plan file.
type TaskSpec struct {
	Key                string         `json:"key"`
	Title              string         `json:"title"`
	Objective          string         `json:"objective"`
	Agent              string         `json:"agent"`
	Dependencies       []string       `json:"dependencies"`
	ReadOnly           bool           `json:"read_only"`
	AllowedPaths       []string       `json:"allowed_paths"`
	AcceptanceCriteria []string       `json:"acceptance_criteria"`
	RequiredTests      [][]string     `json:"required_tests"`
	TimeoutSeconds     *int           `json:"timeout_seconds"`
	MaxRetries         int            `json:"max_retries"`
	Options            map[string]any `json:"options"`
}

// ValidateDAG checks dependency references and cycles; returns topological
// order of keys (stable by key name for equal readiness).
func ValidateDAG(specs []TaskSpec) ([]string, error) {
	keys := make([]string, len(specs))
	byKey := map[string]TaskSpec{}
	for i, s := range specs {
		if s.Key == "" {
			return nil, &ValidationError{Msg: fmt.Sprintf("task #%d is missing 'key'", i+1)}
		}
		keys[i] = s.Key
		if _, dup := byKey[s.Key]; dup {
			return nil, &ValidationError{Msg: fmt.Sprintf("duplicate task key '%s'", s.Key)}
		}
		byKey[s.Key] = s
	}
	deps := map[string][]string{}
	for _, s := range specs {
		for _, d := range s.Dependencies {
			if _, ok := byKey[d]; !ok {
				return nil, &ValidationError{Msg: fmt.Sprintf("task '%s' depends on unknown task '%s'", s.Key, d)}
			}
		}
		deps[s.Key] = s.Dependencies
	}
	var order []string
	remaining := map[string]map[string]bool{}
	for k, ds := range deps {
		set := map[string]bool{}
		for _, d := range ds {
			set[d] = true
		}
		remaining[k] = set
	}
	var ready []string
	for _, k := range keys {
		if len(remaining[k]) == 0 {
			ready = append(ready, k)
		}
	}
	sort.Strings(ready)
	inOrder := map[string]bool{}
	for len(ready) > 0 {
		node := ready[0]
		ready = ready[1:]
		order = append(order, node)
		inOrder[node] = true
		for _, other := range keys {
			if inOrder[other] {
				continue
			}
			if remaining[other][node] {
				delete(remaining[other], node)
				if len(remaining[other]) == 0 {
					ready = append(ready, other)
					sort.Strings(ready)
				}
			}
		}
	}
	if len(order) != len(keys) {
		var unresolved []string
		for _, k := range keys {
			if !inOrder[k] {
				unresolved = append(unresolved, k)
			}
		}
		return nil, &ValidationError{Msg: "dependency cycle detected involving: " + join(unresolved)}
	}
	return order, nil
}

func join(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}

// MarshalJSONColumns is used by the store for JSON-encoded columns.
func MarshalColumn(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
