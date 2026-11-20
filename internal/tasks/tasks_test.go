package tasks

import "testing"

func TestTransitionTable(t *testing.T) {
	task := &Task{ID: "TASK-001", PlanID: "PLAN-001", RepoID: "R-1",
		Title: "t", Objective: "o", Agent: "mock", Status: StatusDraft}
	steps := []string{StatusApprovedForExecution, StatusQueued, StatusRunning,
		StatusNeedsReview, StatusApproved, StatusIntegrated}
	for _, step := range steps {
		if err := task.ApplyTransition(step, ""); err != nil {
			t.Fatalf("legal transition %s failed: %v", step, err)
		}
	}
	if err := task.ApplyTransition(StatusRunning, ""); err == nil {
		t.Fatal("integrated -> running must be illegal")
	}

	blocked := &Task{ID: "TASK-002", Status: StatusApprovedForExecution}
	if err := blocked.ApplyTransition(StatusBlocked, "waiting"); err != nil {
		t.Fatalf("block failed: %v", err)
	}
	if blocked.BlockedReason == "" {
		t.Fatal("blocked reason not recorded")
	}
	if err := blocked.ApplyTransition(StatusQueued, "unblocked"); err != nil {
		t.Fatalf("unblock failed: %v", err)
	}
	if blocked.BlockedReason != "" {
		t.Fatal("blocked reason not cleared")
	}

	failed := &Task{ID: "TASK-003", Status: StatusFailed}
	if err := failed.ApplyTransition(StatusQueued, "retry"); err != nil {
		t.Fatalf("failed -> queued (retry) must be allowed: %v", err)
	}

	review := &Task{ID: "TASK-004", Status: StatusNeedsReview}
	if err := review.ApplyTransition(StatusIntegrated, ""); err == nil {
		t.Fatal("needs_review -> integrated must be illegal (review approval is mandatory)")
	}
	if err := review.ApplyTransition(StatusApproved, ""); err != nil {
		t.Fatalf("needs_review -> approved must be legal: %v", err)
	}
}

func TestValidateDAG(t *testing.T) {
	order, err := ValidateDAG([]TaskSpec{
		{Key: "a"}, {Key: "b", Dependencies: []string{"a"}},
	})
	if err != nil {
		t.Fatalf("valid DAG rejected: %v", err)
	}
	if order[0] != "a" || order[1] != "b" {
		t.Fatalf("unexpected order: %v", order)
	}

	if _, err := ValidateDAG([]TaskSpec{{Key: "a", Dependencies: []string{"a"}}}); err == nil {
		t.Fatal("cycle not detected")
	}
	if _, err := ValidateDAG([]TaskSpec{{Key: "a", Dependencies: []string{"ghost"}}}); err == nil {
		t.Fatal("missing dependency not detected")
	}
	if _, err := ValidateDAG([]TaskSpec{{Key: "a"}, {Key: "a"}}); err == nil {
		t.Fatal("duplicate key not detected")
	}
}
