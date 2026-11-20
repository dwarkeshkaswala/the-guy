package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dwkaswala/the-guy/internal/agents"
	"github.com/dwkaswala/the-guy/internal/config"
	"github.com/dwkaswala/the-guy/internal/gitops"
	"github.com/dwkaswala/the-guy/internal/store"
	"github.com/dwkaswala/the-guy/internal/tasks"
)

// normalizeTaskSpec validates one task from a plan file.
func normalizeTaskSpec(index int, raw map[string]any, defaultAgent string) (tasks.TaskSpec, error) {
	spec := tasks.TaskSpec{Agent: defaultAgent}
	key := stringField(raw, "key", "id")
	if key == "" {
		return spec, fmt.Errorf("task #%d is missing 'key'", index+1)
	}
	spec.Key = key
	spec.Title = stringField(raw, "title", "")
	if spec.Title == "" {
		return spec, fmt.Errorf("task '%s' is missing 'title'", key)
	}
	spec.Objective = stringField(raw, "objective", spec.Title)
	if a := stringField(raw, "agent", ""); a != "" {
		spec.Agent = a
	}
	if deps, ok := raw["dependencies"].([]any); ok {
		for _, d := range deps {
			if s, ok := d.(string); ok {
				spec.Dependencies = append(spec.Dependencies, s)
			} else {
				return spec, fmt.Errorf("task '%s': dependencies must be a list of task keys", key)
			}
		}
	}
	spec.ReadOnly = boolField(raw, "read_only")
	if allowed, ok := raw["allowed_paths"].([]any); ok {
		for _, a := range allowed {
			if s, ok := a.(string); ok {
				spec.AllowedPaths = append(spec.AllowedPaths, s)
			} else {
				return spec, fmt.Errorf("task '%s': allowed_paths must be a list of strings", key)
			}
		}
	}
	for _, c := range listField(raw, "acceptance_criteria") {
		spec.AcceptanceCriteria = append(spec.AcceptanceCriteria, fmt.Sprint(c))
	}
	if tests, ok := raw["required_tests"].([]any); ok {
		for _, t := range tests {
			cmd, ok := t.([]any)
			if !ok || len(cmd) == 0 {
				return spec, fmt.Errorf("task '%s': required_tests must be a list of argv lists, e.g. [[\"python\",\"-m\",\"pytest\"]]", key)
			}
			var argv []string
			for _, a := range cmd {
				s, ok := a.(string)
				if !ok {
					return spec, fmt.Errorf("task '%s': required_tests commands must contain only strings", key)
				}
				argv = append(argv, s)
			}
			spec.RequiredTests = append(spec.RequiredTests, argv)
		}
	} else if raw["required_tests"] != nil {
		return spec, fmt.Errorf("task '%s': required_tests must be a list of argv lists", key)
	}
	if v, ok := raw["timeout_seconds"]; ok && v != nil {
		switch n := v.(type) {
		case float64:
			sec := int(n)
			spec.TimeoutSeconds = &sec
		case int:
			spec.TimeoutSeconds = &n
		default:
			return spec, fmt.Errorf("task '%s': timeout_seconds must be an integer", key)
		}
	}
	if v, ok := raw["max_retries"].(float64); ok {
		spec.MaxRetries = int(v)
	}
	if opts, ok := raw["options"].(map[string]any); ok {
		spec.Options = opts
	}
	if _, err := agents.Get(spec.Agent); err != nil {
		return spec, fmt.Errorf("task '%s': %w", key, err)
	}
	return spec, nil
}

func stringField(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok {
			return v
		}
	}
	return ""
}

func boolField(m map[string]any, key string) bool {
	if v, ok := m[key].(bool); ok {
		return v
	}
	return false
}

func listField(m map[string]any, key string) []any {
	if v, ok := m[key].([]any); ok {
		return v
	}
	return nil
}

// createPlanFromSpec validates and persists a plan with its tasks (draft).
func createPlanFromSpec(st *store.Store, repo *tasks.Repo, spec map[string]any, baseBranch, defaultAgent string) (*tasks.Plan, []*tasks.Task, error) {
	rawTasks, _ := spec["tasks"].([]any)
	if len(rawTasks) == 0 {
		return nil, nil, fmt.Errorf("plan spec must contain a non-empty 'tasks' list")
	}
	specs := make([]tasks.TaskSpec, 0, len(rawTasks))
	for i, raw := range rawTasks {
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("task #%d must be an object", i+1)
		}
		s, err := normalizeTaskSpec(i, m, defaultAgent)
		if err != nil {
			return nil, nil, err
		}
		specs = append(specs, s)
	}
	if _, err := tasks.ValidateDAG(specs); err != nil {
		return nil, nil, err
	}
	base := baseBranch
	if base == "" {
		if b, ok := spec["base_branch"].(string); ok {
			base = b
		}
	}
	if base == "" {
		base = repo.DefaultBranch
	}
	baselineCommit, err := gitops.RevParse(repo.Path, base)
	if err != nil {
		return nil, nil, fmt.Errorf("baseline branch '%s' not found: %w", base, err)
	}
	planID, err := st.NextID("PLAN")
	if err != nil {
		return nil, nil, err
	}
	prompt, _ := spec["prompt"].(string)
	plan := &tasks.Plan{
		ID: planID, RepoID: repo.ID, Prompt: prompt,
		BaselineBranch: base, BaselineCommit: baselineCommit,
		Status: "draft", CreatedAt: tasks.UTCNow(),
	}
	if err := st.InsertPlan(*plan); err != nil {
		return nil, nil, err
	}
	idByKey := map[string]string{}
	for _, s := range specs {
		id, err := st.NextID("TASK")
		if err != nil {
			return nil, nil, err
		}
		idByKey[s.Key] = id
	}
	var created []*tasks.Task
	for _, s := range specs {
		deps := make([]string, 0, len(s.Dependencies))
		for _, d := range s.Dependencies {
			deps = append(deps, idByKey[d])
		}
		t := &tasks.Task{
			ID: idByKey[s.Key], PlanID: plan.ID, RepoID: repo.ID,
			Title: s.Title, Objective: s.Objective, Agent: s.Agent,
			Dependencies: deps, ReadOnly: s.ReadOnly, AllowedPaths: s.AllowedPaths,
			AcceptanceCriteria: s.AcceptanceCriteria, RequiredTests: s.RequiredTests,
			TimeoutSeconds: s.TimeoutSeconds, MaxRetries: s.MaxRetries,
			Options: s.Options, Status: tasks.StatusDraft,
			BaselineBranch: base, BaselineCommit: baselineCommit,
			CreatedAt: tasks.UTCNow(),
		}
		if t.Options == nil {
			t.Options = map[string]any{}
		}
		if err := st.InsertTask(t); err != nil {
			return nil, nil, err
		}
		created = append(created, t)
	}
	_ = st.AppendEvent("plan_created", repo.ID, "", map[string]any{
		"plan": plan.ID, "tasks": len(created),
		"baseline": fmt.Sprintf("%s@%.10s", base, baselineCommit)})
	return plan, created, nil
}

func defaultAgentName(cliAgent string, settings map[string]any) (string, string) {
	if cliAgent != "" {
		if _, err := agents.Get(cliAgent); err != nil {
			return "", err.Error()
		}
		return cliAgent, ""
	}
	if configured := config.SettingsString(settings, "default_agent", ""); configured != "" {
		return configured, ""
	}
	for _, name := range []string{"claude", "codex", "opencode", "droid"} {
		a, err := agents.Get(name)
		if err != nil {
			continue
		}
		report := a.Probe(20e9)
		if report.Installed && report.HeadlessVerified {
			return name, ""
		}
	}
	return "mock", "no verified real agent CLI found; plan defaults to the built-in mock agent (deterministic, no model calls)"
}

func cmdPlan(c *Ctx, p parsed) error {
	repo, err := resolveRepo(c.Store, p.flag("repo"))
	if err != nil {
		return err
	}
	settings := config.EffectiveSettings(nil, repo.Path)
	agentName, warning := defaultAgentName(p.flag("agent"), settings)
	if agentName == "" {
		return fmt.Errorf("%s", warning)
	}
	var spec map[string]any
	source := ""
	switch {
	case p.flag("file") != "":
		data, err := os.ReadFile(p.flag("file"))
		if err != nil {
			return fmt.Errorf("cannot read plan file %s: %v", p.flag("file"), err)
		}
		if err := json.Unmarshal(data, &spec); err != nil {
			return fmt.Errorf("invalid plan JSON in %s: %v", p.flag("file"), err)
		}
		source = "file " + p.flag("file")
	case p.flag("planner") != "":
		return fmt.Errorf("--planner is not yet implemented in the Go port; write a plan JSON (see docs/command-reference.md) or use the Python CLI's planner experimentally")
	default:
		prompt := strings.Join(p.positional, " ")
		if prompt == "" {
			return usage("provide a prompt, a --file plan.json (the Go port does not yet support --planner)")
		}
		title := prompt
		if idx := strings.IndexAny(title, "\r\n"); idx >= 0 {
			title = title[:idx]
		}
		if len(title) > 60 {
			title = title[:60]
		}
		spec = map[string]any{
			"prompt": prompt,
			"tasks": []any{map[string]any{
				"key": "t1", "title": title, "objective": prompt}},
		}
		source = "prompt (single-task draft)"
	}
	plan, created, err := createPlanFromSpec(c.Store, repo, spec, p.flag("base"), agentName)
	if err != nil {
		return err
	}
	lines := []string{
		fmt.Sprintf("created DRAFT plan %s (%s) — nothing has been executed or modified", plan.ID, source),
		fmt.Sprintf("  baseline: %s @ %.10s", plan.BaselineBranch, plan.BaselineCommit),
		"  tasks:",
	}
	for _, t := range created {
		deps := ""
		if len(t.Dependencies) > 0 {
			deps = "  deps: " + strings.Join(t.Dependencies, ", ")
		}
		ro := ""
		if t.ReadOnly {
			ro = " [read-only]"
		}
		lines = append(lines, fmt.Sprintf("    %s: %s (agent: %s%s)%s", t.ID, t.Title, t.Agent, ro, deps))
	}
	lines = append(lines, "  next: review with `guy tasks`, then `guy approve "+plan.ID+"` (draft -> approved_for_execution)")
	if warning != "" {
		lines = append(lines, "  note: "+warning)
	}
	return okErr(c, strings.Join(lines, "\n"), map[string]any{
		"plan": plan, "tasks": created, "warning": warning})
}

// writePlanFileForDemo is a helper used by `guy demo` (and tests).
func writePlanFileForDemo(dir string, spec map[string]any) string {
	path := filepath.Join(dir, "plan.json")
	data, _ := json.MarshalIndent(spec, "", "  ")
	_ = os.WriteFile(path, data, 0o644)
	return path
}
