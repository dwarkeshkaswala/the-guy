// Exported wrappers over the CLI's internal business functions so the
// desktop application service (internal/application) reuses the exact same
// tested code paths as `guy` — no duplicated registry, planning, approval,
// cancellation, or integration logic.
package cli

import (
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
