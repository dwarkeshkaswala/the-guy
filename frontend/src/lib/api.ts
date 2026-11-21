// Typed API layer over the generated Wails bindings. Screens never import
// bindings directly — this is the one place that knows the service surface.
import { Events } from "@wailsio/runtime";
import * as Service from "../../bindings/github.com/dwarkeshkaswala/the-guy/internal/application/service.js";
import * as Desktop from "../../bindings/github.com/dwarkeshkaswala/the-guy/desktopservice.js";
import type {
  Task, Plan, RunRecord, RepoView, HealthReport, PlanValidation, PlanView,
  RunView, LogChunk, DiffView, DoctorReport, CapabilityReport, PlannedLaunch,
  EventEnvelope, TickPayload, RunSummary, RunReceipt, ApproveResult,
  IntegrationPreviewResult, IntegrationResult, ReviewPayload, SettingsPayload,
} from "./types";

export const api = {
  // repos
  listRepositories: (): Promise<RepoView[]> => Service.ListRepositories() as Promise<RepoView[]>,
  addRepository: (path: string, alias: string): Promise<RepoView | null> => Service.AddRepository(path, alias) as Promise<RepoView | null>,
  removeRepository: (ref: string, purge: boolean): Promise<{ removed: string; alias: string; purged: boolean }> => Service.RemoveRepository(ref, purge) as Promise<any>,
  setDefaultRepository: (ref: string): Promise<void> => Service.SetDefaultRepository(ref) as Promise<void>,
  inspectRepository: (ref: string): Promise<Record<string, any>> => Service.InspectRepository(ref) as Promise<any>,

  // agents
  listAgents: (cached: boolean): Promise<CapabilityReport[]> => Service.ListAgents(cached) as Promise<CapabilityReport[]>,
  configureAgent: (name: string, path: string): Promise<void> => Service.ConfigureAgent(name, path) as Promise<void>,

  // doctor / health
  doctor: (repoRef: string): Promise<DoctorReport> => Service.GetDoctorReport(repoRef) as Promise<DoctorReport>,
  health: (): Promise<HealthReport> => Service.GetServiceHealth() as Promise<HealthReport>,

  // planning
  validatePlanSpec: (specJSON: string, repoRef: string, baseBranch: string, defaultAgent: string): Promise<PlanValidation> => Service.ValidatePlanSpec(specJSON, repoRef, baseBranch, defaultAgent) as Promise<PlanValidation>,
  createPlan: (repoRef: string, specJSON: string, baseBranch: string, defaultAgent: string): Promise<{ plan: Plan; tasks: Task[]; warning?: string }> => Service.CreatePlan(repoRef, specJSON, baseBranch, defaultAgent) as Promise<any>,
  listPlans: (repoRef: string): Promise<PlanView[]> => Service.ListPlans(repoRef) as Promise<PlanView[]>,
  getPlan: (planID: string): Promise<{ plan: Plan; tasks: Task[] }> => Service.GetPlan(planID) as Promise<any>,
  listTasks: (repoRef: string, planID: string): Promise<Task[]> => Service.ListTasks(repoRef, planID) as Promise<Task[]>,
  getTask: (taskID: string): Promise<{ task: Task; runs: RunRecord[] }> => Service.GetTask(taskID) as Promise<any>,

  // execution
  approve: (target: string, note: string): Promise<ApproveResult> => Service.Approve(target, note) as Promise<any>,
  rejectTask: (taskID: string, reason: string): Promise<{ task: string; status: string }> => Service.RejectTask(taskID, reason) as Promise<any>,
  dryRun: (repoRef: string, planID: string, taskIDs: string[] | null): Promise<PlannedLaunch[]> => Service.DryRun(repoRef, planID, taskIDs ?? []) as Promise<any>,
  startRun: (repoRef: string, planID: string, taskIDs: string[] | null, maxWorkers: number): Promise<RunReceipt> => Service.StartRun(repoRef, planID, taskIDs ?? [], maxWorkers) as Promise<any>,
  cancelTask: (taskID: string): Promise<{ task: string; status: string }> => Service.CancelTask(taskID) as Promise<any>,
  cleanTask: (taskID: string, deleteBranch: boolean): Promise<{ actions: string[] }> => Service.CleanTask(taskID, deleteBranch) as Promise<any>,

  // runs / logs
  listRuns: (repoRef: string, limit: number): Promise<RunView[]> => Service.ListRuns(repoRef, limit) as Promise<any>,
  getRun: (runID: string): Promise<RunRecord> => Service.GetRun(runID) as Promise<any>,
  readLogRange: (taskID: string, offset: number, maxBytes: number): Promise<LogChunk> => Service.ReadLogRange(taskID, offset, maxBytes) as Promise<any>,

  // review / integration
  getReview: (taskID: string): Promise<ReviewPayload> => Service.GetReview(taskID) as Promise<any>,
  getTaskDiff: (taskID: string, maxKB: number): Promise<DiffView> => Service.GetTaskDiff(taskID, maxKB) as Promise<any>,
  runTests: (taskID: string): Promise<Task["test_results"]> => Service.RunTests(taskID) as Promise<any>,
  previewIntegration: (repoRef: string, planID: string, skipTests: boolean): Promise<IntegrationPreviewResult> => Service.PreviewIntegration(repoRef, planID, skipTests) as Promise<any>,
  integrate: (repoRef: string, planID: string, skipTests: boolean): Promise<IntegrationResult> => Service.Integrate(repoRef, planID, skipTests) as Promise<any>,

  // settings / diagnostics
  getSettings: (): Promise<SettingsPayload> => Service.GetSettings() as Promise<any>,
  updateSetting: (key: string, value: unknown): Promise<void> => Service.UpdateSetting(key, value) as Promise<void>,
  exportDiagnostics: (): Promise<string> => Service.ExportDiagnostics() as Promise<string>,
};

export const desktop = {
  pickRepositoryFolder: (): Promise<string> => Desktop.PickRepositoryFolder(),
  revealPath: (path: string): Promise<string> => Desktop.RevealPath(path),
  appVersion: (): Promise<string> => Desktop.AppVersion(),
  closeWarning: (): Promise<{ count: number; taskIDs: string[] }> => Desktop.CloseWarning() as Promise<any>,
};

/** Subscribe to a Go-emitted event. Returns an unsubscribe function. */
export function onEvent<T>(name: string, handler: (data: T) => void): () => void {
  const off = Events.On(name, (ev: { data: T }) => handler(ev.data));
  return () => {
    try {
      off();
    } catch {
      /* window teardown */
    }
  };
}

export { onEvent as onGuyEvent };
export type { EventEnvelope, TickPayload, RunSummary };
