// Plans & Task Graph: create/validate plan specs (schema-checked in Go),
// inspect the dependency DAG, approve for execution, dry-run, and launch —
// each an explicit step. Planning never modifies anything.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useApp } from "../state/app";
import { api } from "../lib/api";
import {
  Banner, Button, Card, ConfirmDialog, EmptyState, Field, Input, Modal,
  SectionHeader, Select, StatusBadge, Tag, TextArea, Spinner, CopyButton,
} from "../components/ui";
import { DagView } from "../components/DagView";
import type { Plan, PlanValidation, PlanView, Task, PlannedLaunch } from "../lib/types";
import { fmtDate, shortId, truncate } from "../lib/format";
import { PlayCircle, Plus, ShieldQuestion, Workflow, FileJson, Rocket, Ban, Eraser } from "lucide-react";

const TEMPLATE = `{
  "prompt": "Describe the overall goal",
  "tasks": [
    {
      "key": "prep",
      "title": "Prepare the change",
      "objective": "What this task must achieve",
      "agent": "mock",
      "allowed_paths": ["src/**"],
      "acceptance_criteria": ["criterion one"],
      "required_tests": []
    },
    {
      "key": "build",
      "title": "Depends on prep",
      "objective": "Runs only after prep is integrated",
      "agent": "mock",
      "dependencies": ["prep"]
    }
  ]
}`;

export function Plans() {
  const app = useApp();
  const [plans, setPlans] = useState<PlanView[] | null>(null);
  const [selectedID, setSelectedID] = useState<string>("");
  const [detail, setDetail] = useState<{ plan: Plan; tasks: Task[] } | null>(null);
  const [selectedTask, setSelectedTask] = useState<Task | null>(null);
  const [createOpen, setCreateOpen] = useState(false);
  const [dry, setDry] = useState<PlannedLaunch[] | null>(null);
  const [drying, setDrying] = useState(false);
  const [launchConfirm, setLaunchConfirm] = useState<PlannedLaunch[] | null>(null);
  const [launching, setLaunching] = useState(false);
  const [approving, setApproving] = useState(false);

  const repo = app.currentRepo;

  const reload = useCallback(async () => {
    if (!repo) {
      setPlans([]);
      return;
    }
    const list = await api.listPlans(repo.alias).catch(() => []);
    setPlans(list);
    setSelectedID((prev) => (prev && list.some((p) => p.id === prev) ? prev : list[0]?.id ?? ""));
  }, [repo?.alias]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    reload();
  }, [reload, app.refreshTick]);

  useEffect(() => {
    if (!selectedID) {
      setDetail(null);
      return;
    }
    api.getPlan(selectedID).then(setDetail).catch(() => setDetail(null));
  }, [selectedID, app.refreshTick]);

  useEffect(() => {
    const onNew = () => setCreateOpen(true);
    window.addEventListener("guy:new-plan", onNew);
    return () => window.removeEventListener("guy:new-plan", onNew);
  }, []);

  useEffect(() => {
    if (detail && selectedTask) {
      const fresh = detail.tasks.find((t) => t.id === selectedTask.id);
      setSelectedTask(fresh ?? null);
    }
  }, [detail]); // eslint-disable-line react-hooks/exhaustive-deps

  const doDryRun = async () => {
    if (!repo || !detail) return;
    setDrying(true);
    setDry(null);
    try {
      setDry(await api.dryRun(repo.alias, detail.plan.id, null));
    } catch (e) {
      app.toast("fail", String(e));
    } finally {
      setDrying(false);
    }
  };

  const approvePlan = async () => {
    if (!detail) return;
    setApproving(true);
    try {
      const res = await api.approve(detail.plan.id, "plan approval (desktop)");
      app.toast(
        "ok",
        `Plan approved for execution: ${(res.approved_for_execution ?? []).map(shortId).join(", ")}. This authorizes launching agents in isolated worktrees — not merges, commits, or pushes.`,
      );
      await reload();
    } catch (e) {
      app.toast("fail", String(e));
    } finally {
      setApproving(false);
    }
  };

  const launch = async () => {
    if (!repo || !detail) return;
    setLaunching(true);
    try {
      const receipt = await api.startRun(repo.alias, detail.plan.id, null, 0);
      if (receipt.started) {
        app.toast("ok", `Launched ${receipt.tasks?.length ?? 0} task(s) (max ${receipt.max_workers} concurrent). Watch Runs for live output.`);
        setLaunchConfirm(null);
        setDry(null);
        app.setScreen("runs");
        const first = receipt.tasks?.[0];
        if (first) app.openLog(first);
      } else {
        app.toast("info", receipt.reason ?? "no tasks ready");
        setLaunchConfirm(null);
      }
    } catch (e) {
      app.toast("fail", String(e));
    } finally {
      setLaunching(false);
    }
  };

  if (!repo) {
    return (
      <EmptyState
        icon={<Workflow size={28} />}
        title="No repository selected"
        body="Register and select a repository first — plans always belong to one repository."
        action={<Button variant="primary" onClick={() => app.setScreen("repos")}>Go to repositories</Button>}
      />
    );
  }

  return (
    <div className="space-y-4">
      <SectionHeader
        title="Plans & Task Graph"
        subtitle={`${repo.alias} · baseline branch ${repo.default_branch}`}
        actions={
          <>
            <Select
              value={selectedID}
              onChange={(e) => setSelectedID(e.target.value)}
              className="w-56"
              aria-label="Select plan"
            >
              {(plans ?? []).map((p) => (
                <option key={p.id} value={p.id}>
                  {p.id} — {p.status} ({Object.values(p.task_counts ?? {}).reduce((a: number, b) => a + (b as number), 0)} tasks)
                </option>
              ))}
            </Select>
            <Button variant="primary" onClick={() => setCreateOpen(true)}>
              <Plus size={13} /> New plan
            </Button>
          </>
        }
      />

      {plans === null ? (
        <Spinner label="Loading plans…" />
      ) : plans.length === 0 ? (
        <EmptyState
          icon={<Workflow size={28} />}
          title="No plans yet"
          body="A plan is a JSON task graph — objectives, agents, dependencies, tests. Creating a plan changes nothing; execution needs your explicit approval."
          action={
            <Button variant="primary" onClick={() => setCreateOpen(true)}>
              <Plus size={13} /> Create a plan
            </Button>
          }
        />
      ) : detail ? (
        <>
          <PlanActions
            detail={detail}
            approving={approving}
            onApprove={approvePlan}
            onDryRun={doDryRun}
            drying={drying}
            onLaunchFromDry={() => setLaunchConfirm(dry ?? [])}
          />
          {dry !== null && <DryRunPanel launches={dry} onLaunch={() => setLaunchConfirm(dry)} drying={drying} />}

          <Card className="p-4">
            <div className="flex items-center gap-2 mb-3 flex-wrap">
              <h3 className="font-medium text-sm">Task graph</h3>
              <Tag tone="mono">{detail.plan.baseline_branch}@{detail.plan.baseline_commit.slice(0, 8)}</Tag>
              <Tag>{detail.plan.status}</Tag>
              <span className="text-xs text-faint">{detail.plan.prompt && truncate(detail.plan.prompt, 90)}</span>
            </div>
            <DagView tasks={detail.tasks} onSelect={(id) => setSelectedTask(detail.tasks.find((t) => t.id === id) ?? null)} />
          </Card>

          <TaskTable
            tasks={detail.tasks}
            onSelect={(t) => setSelectedTask(t)}
            onApprove={async (t) => {
              try {
                await api.approve(t.id, "task approval (desktop)");
                app.toast("ok", `${t.id} approved`);
              } catch (e) {
                app.toast("fail", String(e));
              }
            }}
            onCancel={async (t) => {
              try {
                await api.cancelTask(t.id);
                app.toast("ok", `${t.id} cancelled`);
              } catch (e) {
                app.toast("fail", String(e));
              }
            }}
            onClean={async (t) => {
              try {
                const r = await api.cleanTask(t.id, false);
                app.toast("ok", r.actions?.join("; ") || "nothing to clean");
              } catch (e) {
                app.toast("fail", String(e));
              }
            }}
            onOpenLog={(t) => app.openLog(t.id)}
          />

          {selectedTask && <TaskDetail task={selectedTask} onClose={() => setSelectedTask(null)} />}
        </>
      ) : (
        <Spinner label="Loading plan…" />
      )}

      <CreatePlanModal
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        repoAlias={repo.alias}
        baseBranch={repo.default_branch}
        onCreated={async (id) => {
          setCreateOpen(false);
          await reload();
          setSelectedID(id);
          app.toast("ok", "Draft plan created — nothing has been executed or modified");
        }}
      />

      <ConfirmDialog
        open={!!launchConfirm}
        onClose={() => setLaunchConfirm(null)}
        onConfirm={launch}
        loading={launching}
        title="Launch approved tasks"
        confirmLabel="Launch now"
        body={
          <div className="space-y-2">
            <p>
              This starts real agent processes in isolated worktrees (one write owner per worktree,
              bounded by max workers). Agents run with their own auth; the-guy adds no bypass flags.
            </p>
            <ul className="text-xs font-mono space-y-1">
              {(launchConfirm ?? []).map((l) => (
                <li key={l.task_id}>
                  {l.task_id} · {l.agent} → {l.read_only ? "(read-only, repo root)" : l.branch}
                  {!l.adapter_ok && <span className="text-fail"> — adapter problem: {l.adapter_note}</span>}
                  {l.pending_dependencies?.length ? (
                    <span className="text-warn"> — blocked by: {(l.pending_dependencies ?? []).join(", ")}</span>
                  ) : null}
                </li>
              ))}
            </ul>
          </div>
        }
      />
    </div>
  );
}

function PlanActions({
  detail, approving, onApprove, onDryRun, drying, onLaunchFromDry,
}: {
  detail: { plan: Plan; tasks: Task[] };
  approving: boolean;
  onApprove: () => void;
  onDryRun: () => void;
  drying: boolean;
  onLaunchFromDry: () => void;
}) {
  const drafts = detail.tasks.filter((t) => t.status === "draft").length;
  const ready = detail.tasks.filter((t) =>
    ["approved_for_execution", "queued", "blocked", "failed"].includes(t.status),
  ).length;
  return (
    <Card className="p-3.5 flex items-center gap-2.5 flex-wrap">
      <ShieldQuestion size={15} className="text-mut" aria-hidden />
      <div className="flex-1 min-w-52 text-sm">
        <span className="font-medium">{detail.plan.id}</span>
        <span className="text-faint"> · created {fmtDate(detail.plan.created_at)} · </span>
        {drafts > 0 ? (
          <span className="text-warn">{drafts} draft task(s) need approval</span>
        ) : ready > 0 ? (
          <span className="text-mut">{ready} task(s) ready to run</span>
        ) : (
          <span className="text-mut">no runnable tasks</span>
        )}
      </div>
      <Button onClick={onApprove} loading={approving} disabled={drafts === 0}>
        <PlayCircle size={13} /> Approve plan
      </Button>
      <Button onClick={onDryRun} loading={drying} disabled={ready === 0}>
        <FileJson size={13} /> Dry run
      </Button>
      <Button variant="primary" onClick={onLaunchFromDry} disabled={ready === 0}>
        <Rocket size={13} /> Launch…
      </Button>
    </Card>
  );
}

function DryRunPanel({
  launches, onLaunch, drying,
}: { launches: PlannedLaunch[]; onLaunch: () => void; drying: boolean }) {
  if (drying) return <Card className="p-4"><Spinner label="Probing adapters and computing proposals…" /></Card>;
  if (launches.length === 0) {
    return (
      <Card className="p-4 text-sm text-mut">
        Dry run: no tasks ready. Approve draft tasks first.
      </Card>
    );
  }
  return (
    <Card className="p-4">
      <div className="flex items-center gap-2 mb-3">
        <h3 className="font-medium text-sm">Dry run — proposals only, nothing executes</h3>
        <div className="flex-1" />
        <Button variant="primary" onClick={onLaunch}>
          <Rocket size={13} /> Launch these tasks…
        </Button>
      </div>
      <ul className="space-y-2.5">
        {launches.map((l) => (
          <li key={l.task_id} className="rounded-md border border-edge bg-inset p-3 text-xs">
            <div className="flex items-center gap-2 flex-wrap mb-1">
              <span className="font-mono text-ink">{l.task_id}</span>
              <span className="font-medium">{l.title}</span>
              <Tag tone="mono">{l.agent}</Tag>
              <Tag>{l.read_only ? "read-only" : "worktree"}</Tag>
              <Tag tone="mono">timeout {l.timeout_seconds}s</Tag>
              {!l.adapter_ok && <Tag tone="fail">adapter: {l.adapter_note}</Tag>}
            </div>
            <div className="font-mono text-faint break-all">
              {l.argv_display || "(command unavailable)"}
            </div>
            {!l.read_only && (
              <div className="font-mono text-faint mt-1">
                branch {l.branch} · worktree {l.worktree}
              </div>
            )}
            {(l.pending_dependencies?.length ?? 0) > 0 && (
              <div className="text-warn mt-1">waiting for dependencies: {(l.pending_dependencies ?? []).join(", ")}</div>
            )}
          </li>
        ))}
      </ul>
    </Card>
  );
}

function TaskTable({
  tasks, onSelect, onApprove, onCancel, onClean, onOpenLog,
}: {
  tasks: Task[];
  onSelect: (t: Task) => void;
  onApprove: (t: Task) => void;
  onCancel: (t: Task) => void;
  onClean: (t: Task) => void;
  onOpenLog: (t: Task) => void;
}) {
  return (
    <Card className="overflow-hidden">
      <table className="w-full text-sm">
        <thead>
          <tr className="text-left text-xs text-faint border-b border-edge">
            <th className="px-3.5 py-2 font-medium">Task</th>
            <th className="px-3 py-2 font-medium">Status</th>
            <th className="px-3 py-2 font-medium">Agent</th>
            <th className="px-3 py-2 font-medium">Deps</th>
            <th className="px-3 py-2 font-medium">Scope</th>
            <th className="px-3 py-2 font-medium text-right">Actions</th>
          </tr>
        </thead>
        <tbody>
          {tasks.map((t) => (
            <tr key={t.id} className="border-b border-edge last:border-b-0 hover:bg-overlay cursor-pointer" onClick={() => onSelect(t)}>
              <td className="px-3.5 py-2">
                <div className="font-mono text-xs text-faint">{shortId(t.id)}</div>
                <div className="font-medium">{truncate(t.title, 60)}</div>
              </td>
              <td className="px-3 py-2"><StatusBadge status={t.status} small /></td>
              <td className="px-3 py-2"><Tag tone="mono">{t.agent}</Tag></td>
              <td className="px-3 py-2 text-xs font-mono text-mut">
                {(t.dependencies ?? []).length ? (t.dependencies ?? []).map(shortId).join(", ") : "—"}
              </td>
              <td className="px-3 py-2 text-xs text-mut">
                {t.read_only ? "read-only" : t.allowed_paths?.length ? t.allowed_paths.join(", ") : "whole repo"}
              </td>
              <td className="px-3 py-2 text-right" onClick={(e) => e.stopPropagation()}>
                <div className="inline-flex gap-1.5">
                  {(t.status === "draft" || t.status === "needs_review") && (
                    <Button size="sm" onClick={() => onApprove(t)}>Approve</Button>
                  )}
                  {["queued", "running", "approved_for_execution"].includes(t.status) && (
                    <Button size="sm" variant="danger" onClick={() => onCancel(t)}>
                      <Ban size={11} /> Cancel
                    </Button>
                  )}
                  {["failed", "cancelled", "integrated"].includes(t.status) && (
                    <Button size="sm" onClick={() => onClean(t)}>
                      <Eraser size={11} /> Clean
                    </Button>
                  )}
                  <Button size="sm" variant="ghost" onClick={() => onOpenLog(t)}>Log</Button>
                </div>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </Card>
  );
}

function TaskDetail({ task, onClose }: { task: Task; onClose: () => void }) {
  return (
    <Modal open onClose={onClose} title={`${task.id} — ${task.title}`} wide>
      <div className="space-y-3.5 text-sm">
        <div className="flex items-center gap-2 flex-wrap">
          <StatusBadge status={task.status} />
          <Tag tone="mono">{task.agent}</Tag>
          {task.read_only && <Tag>read-only</Tag>}
          {task.branch && <Tag tone="mono">{task.branch}</Tag>}
        </div>
        <div>
          <div className="text-xs font-medium text-mut mb-1">Objective</div>
          <p className="whitespace-pre-wrap leading-relaxed">{task.objective}</p>
        </div>
        {(task.acceptance_criteria?.length ?? 0) > 0 && (
          <div>
            <div className="text-xs font-medium text-mut mb-1">Acceptance criteria</div>
            <ul className="list-disc pl-5 space-y-0.5">
              {(task.acceptance_criteria ?? []).map((c, i) => <li key={i}>{c}</li>)}
            </ul>
          </div>
        )}
        {(task.required_tests?.length ?? 0) > 0 && (
          <div>
            <div className="text-xs font-medium text-mut mb-1">Required tests (run explicitly from Review)</div>
            <ul className="font-mono text-xs space-y-0.5">
              {(task.required_tests ?? []).map((cmd, i) => <li key={i}>{JSON.stringify(cmd)}</li>)}
            </ul>
          </div>
        )}
        {task.blocked_reason && <Banner kind="warn">{task.blocked_reason}</Banner>}
        {task.exit_reason && <Banner kind={task.status === "needs_review" || task.status === "integrated" ? "ok" : "fail"}>{task.exit_reason}</Banner>}
        <div>
          <div className="text-xs font-medium text-mut mb-1">Fields</div>
          <dl className="text-xs space-y-1">
            <div className="flex gap-2"><dt className="text-faint w-32">Baseline</dt><dd className="font-mono">{task.baseline_branch}@{task.baseline_commit?.slice(0, 10)}</dd></div>
            <div className="flex gap-2"><dt className="text-faint w-32">Worktree</dt><dd className="font-mono break-all">{task.worktree || "—"}</dd></div>
            <div className="flex gap-2"><dt className="text-faint w-32">Allowed paths</dt><dd className="font-mono">{task.allowed_paths?.join(", ") || "(whole repo)"}</dd></div>
            <div className="flex gap-2"><dt className="text-faint w-32">Changed files</dt><dd className="font-mono break-all">{task.changed_files?.join(", ") || "—"}</dd></div>
            <div className="flex gap-2"><dt className="text-faint w-32">Exit</dt><dd className="font-mono">{task.exit_code != null ? `code ${task.exit_code}` : "—"} {task.exit_reason ? `(${task.exit_reason})` : ""}</dd></div>
          </dl>
        </div>
        {task.worktree && (
          <div className="flex justify-end"><CopyButton text={task.worktree} label="Copy worktree path" /></div>
        )}
      </div>
    </Modal>
  );
}

function CreatePlanModal({
  open, onClose, repoAlias, baseBranch, onCreated,
}: {
  open: boolean;
  onClose: () => void;
  repoAlias: string;
  baseBranch: string;
  onCreated: (planID: string) => void;
}) {
  const app = useApp();
  const [spec, setSpec] = useState(TEMPLATE);
  const [base, setBase] = useState("");
  const [agent, setAgent] = useState("");
  const [validation, setValidation] = useState<PlanValidation | null>(null);
  const [creating, setCreating] = useState(false);
  const [error, setError] = useState("");
  const timer = useRef<number | undefined>(undefined);

  useEffect(() => {
    if (open) {
      setBase(baseBranch);
      setAgent("");
      setError("");
    }
  }, [open, baseBranch]);

  useEffect(() => {
    window.clearTimeout(timer.current);
    timer.current = window.setTimeout(async () => {
      try {
        setValidation(await api.validatePlanSpec(spec, repoAlias, base, agent));
      } catch (e) {
        setValidation(null);
      }
    }, 350);
    return () => window.clearTimeout(timer.current);
  }, [spec, repoAlias, base, agent]);

  const agents = useMemo(() => validation?.agent_choices ?? [], [validation]);

  const create = async () => {
    setCreating(true);
    setError("");
    try {
      const res = await api.createPlan(repoAlias, spec, base, agent);
      if (res.warning) app.toast("info", res.warning);
      onCreated(res.plan.id);
    } catch (e) {
      setError(String(e));
    } finally {
      setCreating(false);
    }
  };

  return (
    <Modal
      open={open}
      onClose={onClose}
      title="New plan"
      wide
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" onClick={create} loading={creating} disabled={!validation?.ok}>
            Create draft plan
          </Button>
        </>
      }
    >
      <div className="space-y-3.5">
        <Banner kind="info">
          Creating a plan writes task definitions only — nothing is executed. The Go backend
          revalidates the schema, agents, dependency graph, and baseline before saving.
        </Banner>
        <div className="grid grid-cols-2 gap-3">
          <Field label="Baseline branch" hint="Worktrees branch from this commit.">
            <Input value={base} onChange={(e) => setBase(e.target.value)} className="font-mono" />
          </Field>
          <Field label="Default agent" hint={validation?.agent_warning || "Used for tasks without an explicit agent."}>
            <Select value={agent} onChange={(e) => setAgent(e.target.value)}>
              <option value="">(auto — first verified)</option>
              {agents.map((a) => <option key={a} value={a}>{a}</option>)}
            </Select>
          </Field>
        </div>
        <Field label="Plan JSON">
          <TextArea
            value={spec}
            onChange={(e) => setSpec(e.target.value)}
            rows={14}
            spellCheck={false}
            aria-label="Plan JSON"
          />
        </Field>
        {validation && !validation.ok && (validation.errors?.length ?? 0) > 0 && (
          <Banner kind="fail">
            <ul className="list-disc pl-4 space-y-0.5 font-mono text-xs">
              {(validation.errors ?? []).map((e, i) => <li key={i}>{e}</li>)}
            </ul>
          </Banner>
        )}
        {validation?.ok && (
          <Banner kind="ok">
            Valid — {(validation.tasks ?? []).length} task(s), execution order: {(validation.topo_order ?? []).join(" → ")}
          </Banner>
        )}
        {error && <Banner kind="fail">{error}</Banner>}
      </div>
    </Modal>
  );
}
