// Exported wrappers over the CLI's internal business functions so the
// desktop application service (internal/application) reuses the exact same
// tested code paths as `guy` — no duplicated registry, planning, approval,
// cancellation, or integration logic.
package cli

import (
	"fmt"

	"github.com/dwkaswala/the-guy/internal/store"
	"github.com/dwkaswala/the-guy/internal/tasks"
)

func RegisterRepo(st *store.Store, path, alias string) (*tasks.Repo, error) {
	return registerRepo(st, path, alias)
}

func RemoveRepo(st *store.Store, ref string, purge bool) (*tasks.Repo, error) {
	return removeRepo(st, ref, purge)
}

func ResolveRepo(st *store.Store, ref string) (*tasks.Repo, error) {
	return resolveRepo(st, ref)
}

func InspectRepo(st *store.Store, repo *tasks.Repo) map[string]any {
	return inspectRepo(st, repo)
}

func CreatePlanFromSpec(st *store.Store, repo *tasks.Repo, spec map[string]any, baseBranch, defaultAgent string) (*tasks.Plan, []*tasks.Task, error) {
	return createPlanFromSpec(st, repo, spec, baseBranch, defaultAgent)
}

func NormalizeTaskSpec(index int, raw map[string]any, defaultAgent string) (tasks.TaskSpec, error) {
	return normalizeTaskSpec(index, raw, defaultAgent)
}

func DefaultAgentName(cliAgent string, settings map[string]any) (string, string) {
	return defaultAgentName(cliAgent, settings)
}

func DoApprove(st *store.Store, target, note string) (map[string]any, error) {
	return doApprove(st, target, note)
}

func DoCancel(st *store.Store, taskID string) (*tasks.Task, error) {
	return doCancel(st, taskID)
}

func SelectRunTasks(st *store.Store, repo *tasks.Repo, planID string, taskIDs []string) ([]*tasks.Task, error) {
	return selectRunTasks(st, repo, planID, taskIDs)
}

func CleanTask(st *store.Store, repo *tasks.Repo, task *tasks.Task, deleteBranch bool) ([]string, error) {
	return cleanTask(st, repo, task, deleteBranch)
}

func PlanForIntegrate(st *store.Store, repo *tasks.Repo, planID string) (*tasks.Plan, error) {
	return planForIntegrate(st, repo, planID)
}

// PlanWithPlanner runs `--planner` for non-CLI front ends (desktop app):
// scratch-worktree planner run, sanitize, validate, persist as a DRAFT plan.
func PlanWithPlanner(st *store.Store, repo *tasks.Repo, plannerName, goal, baseBranch, agentName string, settings map[string]any) (*tasks.Plan, []*tasks.Task, map[string]any, error) {
	planned, err := runPlanner(st, repo, plannerName, goal, baseBranch, settings)
	if err != nil {
		return nil, nil, nil, err
	}
	plan, created, err := createPlanFromSpec(st, repo, planned.Spec, baseBranch, agentName)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("planner output failed validation: %w; log: %s", err, planned.LogPath)
	}
	_ = st.AppendEvent("planner_finished", repo.ID, "", map[string]any{
		"plan": plan.ID, "planner": planned.Planner, "log": planned.LogPath,
		"tasks": len(created), "notes": planned.Warnings})
	return plan, created, map[string]any{
		"agent": planned.Planner, "log": planned.LogPath, "notes": planned.Warnings}, nil
}
