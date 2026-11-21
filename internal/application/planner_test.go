package application

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/dwkaswala/the-guy/internal/tasks"
)

func TestCreatePlanWithPlannerFromDesktop(t *testing.T) {
	svc := newEnv(t)
	repoPath := makeRepo(t, filepath.Join(t.TempDir(), "gui-planned"), true)
	if _, err := svc.AddRepository(repoPath, "gui"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GUY_MOCK_PLANNER_SPEC", `{"stdout": "{\"tasks\": [{\"key\": \"a\", \"title\": \"A\", \"allowed_paths\": [\"a/\"]}, {\"key\": \"b\", \"title\": \"B\", \"dependencies\": [\"a\"], \"allowed_paths\": [\"b/\"]}]}"}`)

	res, err := svc.CreatePlanWithPlanner("gui", "Build A then B", "mock", "", "")
	if err != nil {
		t.Fatal(err)
	}
	created := res["tasks"].([]*tasks.Task)
	if len(created) != 2 || created[1].Dependencies[0] != created[0].ID || created[0].Status != tasks.StatusDraft {
		t.Fatalf("unexpected tasks: %+v", created)
	}
	if res["planner"].(map[string]any)["log"].(string) == "" {
		t.Error("planner log path missing")
	}
	sink := svc.sink.(*captureSink)
	if names := strings.Join(sink.names(), ","); strings.Count(names, "guy:planner") != 2 {
		t.Errorf("want running+done planner events, got %s", names)
	}

	t.Setenv("GUY_MOCK_PLANNER_SPEC", `{"stdout": "no plan today"}`)
	if _, err := svc.CreatePlanWithPlanner("gui", "Nothing", "mock", "", ""); err == nil || !strings.Contains(err.Error(), "no JSON object") {
		t.Fatalf("want planner failure, got %v", err)
	}
}
