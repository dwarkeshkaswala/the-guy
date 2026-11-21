// Shared UI types. Struct-backed shapes come from the generated Wails models;
// map-typed service results get explicit local interfaces so screens stay typed.
import type { Task, RunRecord, TestResult } from "../../bindings/github.com/dwarkeshkaswala/the-guy/internal/tasks/models.js";
import type { RepoView, HealthReport, PlanValidation, PlanView, RunView, LogChunk, DiffView, DoctorReport } from "../../bindings/github.com/dwarkeshkaswala/the-guy/internal/application/models.js";
import type { CapabilityReport } from "../../bindings/github.com/dwarkeshkaswala/the-guy/internal/agents/models.js";
import type { PlannedLaunch } from "../../bindings/github.com/dwarkeshkaswala/the-guy/internal/scheduler/models.js";
import type { DoctorCheck } from "../../bindings/github.com/dwarkeshkaswala/the-guy/internal/cli/models.js";

// Plan is returned inside map-typed payloads, so the generator never sees it —
// mirrored here from internal/tasks (id, repo_id, prompt, baselines, status).
export interface Plan {
  id: string;
  repo_id: string;
  prompt: string;
  baseline_branch: string;
  baseline_commit: string;
  status: string;
  created_at: string;
}

export type { Task, RunRecord, TestResult, RepoView, HealthReport, PlanValidation, PlanView, RunView, LogChunk, DiffView, DoctorReport, CapabilityReport, PlannedLaunch, DoctorCheck };

export interface EventEnvelope {
  seq: number;
  ts: string;
  kind: string;
  repo_id: string;
  task_id: string;
  payload: Record<string, unknown>;
  version: number;
}

export interface TickPayload {
  time: string;
  active_runs: number;
}

export interface SummaryEntry {
  task?: string;
  reason?: string;
  [k: string]: unknown;
}

export interface RunSummary {
  summary: {
    launched?: SummaryEntry[];
    needs_review?: SummaryEntry[];
    failed?: SummaryEntry[];
    cancelled?: SummaryEntry[];
    blocked?: SummaryEntry[];
    retried?: SummaryEntry[];
  };
  finished_at: string;
}

export interface RunReceipt {
  started: boolean;
  tasks?: string[];
  max_workers?: number;
  reason?: string;
}

export interface ApproveResult {
  kind?: string;
  target?: string;
  message?: string;
  approved_for_execution?: string[];
  note?: string;
}

export interface IntegrationPreviewResult {
  plan: string;
  baseline: { branch: string; commit: string };
  integration_branch: string;
  integration_worktree: string;
  actions: string[];
  per_task: { task: string; status: string; actions: string[]; conflict: string | null }[];
  pending: string[];
  predicted_conflicts: { task: string; detail: string }[];
  gate_failures: string[];
  skip_tests: boolean;
  ready: boolean;
}

export interface IntegrationResult {
  status: string;
  integration_branch?: string;
  integration_worktree?: string;
  integrated?: string[];
  pending?: string[];
  message?: string;
  note?: string;
}

export interface ReviewPayload {
  task?: string;
  status?: string;
  changed_files?: string[];
  dirty_in_worktree?: string[];
  commits?: string[];
  diff_stat?: string;
  runs?: { id: string; exit_code: number | null; failure_class: string; log_path: string; duration_ms: number }[];
  handoff_md?: string;
  handoff?: Record<string, unknown>;
  [k: string]: unknown;
}

export interface SettingsPayload {
  settings: Record<string, unknown>;
  known_keys: string[];
  defaults: Record<string, unknown>;
}

export type TaskStatus =
  | "draft"
  | "approved_for_execution"
  | "queued"
  | "running"
  | "needs_review"
  | "approved"
  | "integrated"
  | "blocked"
  | "failed"
  | "cancelled";
