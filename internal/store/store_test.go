package store

import (
	"path/filepath"
	"testing"

	"github.com/dwarkeshkaswala/the-guy/internal/tasks"
)

func TestRoundtripAndSequences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SchemaCompatible(); err != nil {
		t.Fatal(err)
	}

	id, err := st.NextID("TASK")
	if err != nil || id != "TASK-001" {
		t.Fatalf("first id: %q %v", id, err)
	}
	id2, _ := st.NextID("TASK")
	if id2 != "TASK-002" {
		t.Fatalf("second id: %q", id2)
	}

	timeout := 120
	exit := 3
	task := &tasks.Task{
		ID: id, PlanID: "PLAN-001", RepoID: "R-1", Title: "t", Objective: "ünïcode o",
		Agent: "mock", Dependencies: []string{"TASK-000"}, AllowedPaths: []string{"src/**"},
		RequiredTests:  [][]string{{"go", "test", "./..."}},
		TimeoutSeconds: &timeout,
		ExitCode:       &exit,
		Options:        map[string]any{"mock": map[string]any{"exit": 1.0}},
		Status:         tasks.StatusDraft,
		BaselineBranch: "trunk", BaselineCommit: "abc123def4567890",
		CreatedAt: tasks.UTCNow(),
	}
	if err := st.InsertTask(task); err != nil {
		t.Fatal(err)
	}
	loaded, err := st.GetTask(id)
	if err != nil || loaded == nil {
		t.Fatalf("load: %v %v", loaded, err)
	}
	if loaded.Objective != "ünïcode o" || loaded.Dependencies[0] != "TASK-000" ||
		len(loaded.RequiredTests) != 1 || loaded.TimeoutSeconds == nil || *loaded.TimeoutSeconds != 120 ||
		loaded.ExitCode == nil || *loaded.ExitCode != 3 || loaded.Status != tasks.StatusDraft {
		t.Fatalf("roundtrip mismatch: %+v", loaded)
	}
	loaded.Status = tasks.StatusQueued
	if err := st.UpdateTask(loaded); err != nil {
		t.Fatal(err)
	}
	again, _ := st.GetTask(id)
	if again.Status != tasks.StatusQueued {
		t.Fatal("update lost")
	}
}

func TestRepoResolutionByAliasAndPath(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	dir := t.TempDir()
	repo := tasks.Repo{ID: "R-abc", Path: filepath.ToSlash(dir), Alias: "Sample", DefaultBranch: "trunk"}
	if err := st.UpsertRepo(repo); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"R-abc", "sample", "SAMPLE", dir} {
		got, err := st.GetRepo(ref)
		if err != nil || got == nil || got.ID != "R-abc" {
			t.Fatalf("resolve %q: %+v %v", ref, got, err)
		}
	}
	if got, _ := st.GetRepo("missing"); got != nil {
		t.Fatal("unknown ref resolved")
	}
}

func TestEventsAndApprovals(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.AppendEvent("kind_of_thing", "R-1", "TASK-001", map[string]any{"k": "v"}); err != nil {
		t.Fatalf("append event: %v", err)
	}
	events, err := st.RecentEvents(5, "", "")
	if err != nil || len(events) != 1 || events[0].Kind != "kind_of_thing" || events[0].Payload["k"] != "v" {
		t.Fatalf("events: %+v %v", events, err)
	}
	approvalID, _ := st.NextID("APR")
	if err := st.AddApproval(tasks.ApprovalRecord{ID: approvalID, TargetKind: "task",
		TargetID: "TASK-001", Kind: "execute", CreatedAt: tasks.UTCNow()}); err != nil {
		t.Fatal(err)
	}
	ok, err := st.HasApproval("task", "TASK-001", "execute")
	if err != nil || !ok {
		t.Fatalf("has approval: %v %v", ok, err)
	}
}
