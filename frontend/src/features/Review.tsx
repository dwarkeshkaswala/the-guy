// Review & approvals: evidence-first review (real diff, tests, handoff),
// approve/reject of task results — deliberately separate from integration,
// which has its own preview and explicit confirmation. Approval never merges.
import { useCallback, useEffect, useState } from "react";
import { useApp } from "../state/app";
import { api } from "../lib/api";
import {
  Banner, Button, Card, EmptyState, Field, Modal,
  SectionHeader, Spinner, StatusBadge, Tag, TextArea, KeyValue,
} from "../components/ui";
import { DiffViewer } from "../components/DiffViewer";
import type { DiffView, IntegrationPreviewResult, IntegrationResult, ReviewPayload, Task, TestResult } from "../lib/types";
import { fmtDuration } from "../lib/format";
import { CheckCircle2, GitMerge, FlaskConical, XCircle, ClipboardList } from "lucide-react";

export function Review() {
  const app = useApp();
  const [tasks, setTasks] = useState<Task[] | null>(null);
  const [selected, setSelected] = useState<Task | null>(null);
  const [review, setReview] = useState<ReviewPayload | null>(null);
  const [diff, setDiff] = useState<DiffView | null>(null);
  const [tests, setTests] = useState<TestResult[] | null>(null);
  const [loadingEvidence, setLoadingEvidence] = useState(false);
  const [runningTests, setRunningTests] = useState(false);
  const [rejectOpen, setRejectOpen] = useState(false);
  const [rejectReason, setRejectReason] = useState("");
  const [rejecting, setRejecting] = useState(false);
  const [integrOpen, setIntegrOpen] = useState(false);
  const [integrPreview, setIntegrPreview] = useState<IntegrationPreviewResult | null>(null);
  const [integrating, setIntegrating] = useState(false);

  const repo = app.currentRepo;

  const reload = useCallback(async () => {
    if (!repo) {
      setTasks([]);
      return;
    }
    const all = await api.listTasks(repo.alias, "").catch(() => []);
    setTasks(all);
    setSelected((prev) => {
      if (prev && all.some((t) => t.id === prev.id && t.status === prev.status)) {
        return all.find((t) => t.id === prev.id) ?? null;
      }
      return all.find((t) => t.status === "needs_review") ?? null;
    });
  }, [repo?.alias]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    reload();
  }, [reload, app.refreshTick]);

  const loadEvidence = useCallback(async (task: Task) => {
    setLoadingEvidence(true);
    setReview(null);
    setDiff(null);
    setTests(null);
    try {
      const [rv, dv] = await Promise.all([
        api.getReview(task.id),
        api.getTaskDiff(task.id, 512),
      ]);
      setReview(rv);
      setDiff(dv);
    } catch (e) {
      app.toast("fail", String(e));
    } finally {
      setLoadingEvidence(false);
    }
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (selected) loadEvidence(selected);
  }, [selected?.id, selected?.status]); // eslint-disable-line react-hooks/exhaustive-deps

  if (!repo) {
    return (
      <EmptyState
        icon={<ClipboardList size={28} />}
        title="No repository selected"
        body="Select a repository to review its tasks."
        action={<Button variant="primary" onClick={() => app.setScreen("repos")}>Go to repositories</Button>}
      />
    );
  }

  const queue = (tasks ?? []).filter((t) => t.status === "needs_review");
  const reviewed = (tasks ?? []).filter((t) => ["approved", "integrated"].includes(t.status));

  const approveReview = async () => {
    if (!selected) return;
    try {
      await api.approve(selected.id, "review approved (desktop)");
      app.toast(
        "ok",
        `${selected.id}: reviewed. This records human review — nothing was merged or pushed. Integration is a separate, explicit step.`,
      );
      await reload();
    } catch (e) {
      app.toast("fail", String(e));
    }
  };

  const doReject = async () => {
    if (!selected) return;
    setRejecting(true);
    try {
      await api.rejectTask(selected.id, rejectReason || "no reason given");
      app.toast("ok", `${selected.id} rejected → failed`);
      setRejectOpen(false);
      setRejectReason("");
      await reload();
    } catch (e) {
      app.toast("fail", String(e));
    } finally {
      setRejecting(false);
    }
  };

  const runTests = async () => {
    if (!selected) return;
    setRunningTests(true);
    try {
      const results = await api.runTests(selected.id);
      setTests(results ?? []);
      const ok = (results ?? []).every((r) => r.ok);
      app.toast(ok ? "ok" : "fail", `Tests: ${(results ?? []).filter((r) => r.ok).length}/${(results ?? []).length} passed`);
    } catch (e) {
      app.toast("fail", String(e));
    } finally {
      setRunningTests(false);
    }
  };

  const openIntegration = async () => {
    try {
      const preview = await api.previewIntegration(repo.alias, "", false);
      setIntegrPreview(preview);
      setIntegrOpen(true);
    } catch (e) {
      app.toast("fail", String(e));
    }
  };

  const doIntegrate = async () => {
    setIntegrating(true);
    try {
      const res: IntegrationResult = await api.integrate(repo.alias, "", false);
      if (res.status === "conflict") {
        app.toast("fail", `Integration conflict: ${res.message}`);
      } else {
        app.toast("ok", `Integrated into ${res.integration_branch}. Nothing was pushed; the default branch was not modified.`);
      }
      setIntegrOpen(false);
      await reload();
    } catch (e) {
      app.toast("fail", String(e));
    } finally {
      setIntegrating(false);
    }
  };

  return (
    <div className="space-y-4">
      <SectionHeader
        title="Review & Approvals"
        subtitle={`${repo.alias} — approve work, then integrate deliberately. Approval ≠ merge.`}
        actions={
          <Button onClick={openIntegration} disabled={reviewed.length === 0}>
            <GitMerge size={13} /> Integration…
          </Button>
        }
      />

      {tasks === null ? (
        <Spinner label="Loading review queue…" />
      ) : queue.length === 0 && reviewed.length === 0 ? (
        <EmptyState
          icon={<ClipboardList size={28} />}
          title="Nothing to review"
          body="Tasks that finish successfully land here with their diff, test evidence and handoff for human judgment."
        />
      ) : (
        <div className="grid lg:grid-cols-[300px_1fr] gap-4 items-start">
          {/* queue */}
          <div className="space-y-2">
            {queue.length > 0 && (
              <div className="text-xs font-medium text-mut px-1">Needs review ({queue.length})</div>
            )}
            {queue.map((t) => (
              <button
                key={t.id}
                onClick={() => setSelected(t)}
                className={`w-full text-left rounded-lg border p-3 transition-colors ${
                  selected?.id === t.id ? "border-accent bg-accent-soft" : "border-edge bg-raised hover:border-edge-strong"
                }`}
              >
                <div className="flex items-center gap-2">
                  <StatusBadge status={t.status} small />
                  <span className="font-mono text-[11px] text-faint">{t.id}</span>
                </div>
                <div className="text-sm font-medium mt-1 leading-snug">{t.title}</div>
                <div className="text-[11px] text-faint mt-0.5 font-mono">{t.agent} · {t.changed_files?.length ?? 0} changed file(s)</div>
              </button>
            ))}
            {reviewed.length > 0 && (
              <div className="text-xs font-medium text-mut px-1 pt-2">Reviewed ({reviewed.length})</div>
            )}
            {reviewed.map((t) => (
              <button
                key={t.id}
                onClick={() => setSelected(t)}
                className={`w-full text-left rounded-lg border p-3 transition-colors ${
                  selected?.id === t.id ? "border-accent bg-accent-soft" : "border-edge bg-raised hover:border-edge-strong"
                }`}
              >
                <div className="flex items-center gap-2">
                  <StatusBadge status={t.status} small />
                  <span className="font-mono text-[11px] text-faint">{t.id}</span>
                </div>
                <div className="text-sm font-medium mt-1 leading-snug">{t.title}</div>
              </button>
            ))}
          </div>

          {/* evidence */}
          <div className="space-y-4 min-w-0">
            {!selected ? (
              <Card className="p-6 text-sm text-mut">Select a task to review its evidence.</Card>
            ) : loadingEvidence ? (
              <Spinner label="Collecting diff, tests and handoff…" />
            ) : (
              <>
                <Card className="p-4 flex items-center gap-2.5 flex-wrap">
                  <StatusBadge status={selected.status} />
                  <span className="font-semibold">{selected.title}</span>
                  <Tag tone="mono">{selected.id}</Tag>
                  <Tag tone="mono">{selected.agent}</Tag>
                  <div className="flex-1" />
                  {selected.status === "needs_review" && (
                    <>
                      {(selected.required_tests?.length ?? 0) > 0 && (
                        <Button onClick={runTests} loading={runningTests}>
                          <FlaskConical size={13} /> Run tests
                        </Button>
                      )}
                      <Button onClick={() => setRejectOpen(true)} variant="danger">
                        <XCircle size={13} /> Reject
                      </Button>
                      <Button variant="primary" onClick={approveReview}>
                        <CheckCircle2 size={13} /> Approve review
                      </Button>
                    </>
                  )}
                </Card>

                {(selected.required_tests?.length ?? 0) > 0 && (
                  <TestEvidence
                    task={selected}
                    results={tests ?? review?.handoff?.test_results as TestResult[] | undefined ?? null}
                  />
                )}

                <DiffViewer diff={diff ?? { available: false } as DiffView} />

                {review?.handoff_md && (
                  <Card className="p-4">
                    <h3 className="text-sm font-medium mb-2">Handoff summary</h3>
                    <pre className="text-xs leading-relaxed whitespace-pre-wrap font-mono text-mut">
                      {String(review.handoff_md)}
                    </pre>
                  </Card>
                )}

                {Array.isArray(review?.handoff?.concerns) && (review!.handoff!.concerns as string[]).length > 0 && (
                  <Banner kind="warn">
                    <ul className="list-disc pl-4 space-y-0.5 text-xs">
                      {(review!.handoff!.concerns as string[]).map((c, i) => <li key={i}>{c}</li>)}
                    </ul>
                  </Banner>
                )}

                <Card className="p-4">
                  <h3 className="text-sm font-medium mb-2.5">Runs</h3>
                  <ul className="text-xs space-y-1">
                    {(review?.runs ?? []).map((r) => (
                      <li key={r.id} className="flex items-center gap-2 font-mono">
                        <span>{r.id}</span>
                        <span className={r.exit_code === 0 ? "text-ok" : "text-fail"}>exit {r.exit_code ?? "—"}</span>
                        <span className="text-faint">{fmtDuration(r.duration_ms)}</span>
                        {r.log_path && (
                          <button className="text-accent-ink hover:underline" onClick={() => app.openLog(selected.id)}>
                            log
                          </button>
                        )}
                      </li>
                    ))}
                    {(review?.runs ?? []).length === 0 && <li className="text-faint">no runs recorded</li>}
                  </ul>
                </Card>
              </>
            )}
          </div>
        </div>
      )}

      {/* reject dialog */}
      <Modal
        open={rejectOpen}
        onClose={() => setRejectOpen(false)}
        title={`Reject ${selected?.id ?? ""}`}
        footer={
          <>
            <Button onClick={() => setRejectOpen(false)}>Cancel</Button>
            <Button variant="danger" onClick={doReject} loading={rejecting}>Reject task</Button>
          </>
        }
      >
        <Field label="Reason (recorded in the audit trail)">
          <TextArea value={rejectReason} onChange={(e) => setRejectReason(e.target.value)} rows={3} />
        </Field>
      </Modal>

      {/* integration dialog */}
      <Modal
        open={integrOpen}
        onClose={() => setIntegrOpen(false)}
        title="Integration — merge approved tasks"
        wide
        footer={
          <>
            <Button onClick={() => setIntegrOpen(false)}>Close</Button>
            <Button
              variant="primary"
              onClick={doIntegrate}
              loading={integrating}
              disabled={!integrPreview?.ready}
            >
              <GitMerge size={13} /> Integrate (explicit approval)
            </Button>
          </>
        }
      >
        {integrPreview ? (
          <div className="space-y-3.5 text-sm">
            <KeyValue
              items={[
                ["Integration branch", integrPreview.integration_branch],
                ["Integration worktree", integrPreview.integration_worktree],
                ["Baseline", `${integrPreview.baseline.branch}@${integrPreview.baseline.commit.slice(0, 10)}`],
              ]}
            />
            <div>
              <div className="text-xs font-medium text-mut mb-1">Planned actions</div>
              <ul className="text-xs font-mono space-y-1 list-disc pl-4">
                {integrPreview.actions.map((a, i) => <li key={i}>{a}</li>)}
              </ul>
            </div>
            {integrPreview.predicted_conflicts.length > 0 && (
              <Banner kind="warn">
                Predicted conflicts: {integrPreview.predicted_conflicts.map((c) => `${c.task} (${c.detail})`).join("; ")}
              </Banner>
            )}
            {integrPreview.gate_failures.length > 0 ? (
              <Banner kind="fail">
                Gates failed — integration refuses:
                <ul className="list-disc pl-4 mt-1 space-y-0.5 text-xs">
                  {integrPreview.gate_failures.map((g, i) => <li key={i}>{g}</li>)}
                </ul>
              </Banner>
            ) : (
              <Banner kind="ok">
                All gates pass. This merges approved task branches into <b>{integrPreview.integration_branch}</b> only —
                never the default branch, and nothing is pushed.
              </Banner>
            )}
          </div>
        ) : (
          <Spinner label="Computing integration preview…" />
        )}
      </Modal>
    </div>
  );
}

function TestEvidence({ task, results }: { task: Task; results: TestResult[] | null }) {
  const required = task.required_tests ?? [];
  // (defensive: Go may marshal nil slices as null)
  const byCommand = new Map((results ?? []).map((r) => [r.command, r]));
  return (
    <Card className="p-4">
      <h3 className="text-sm font-medium mb-2">Test evidence</h3>
      <ul className="space-y-1.5 text-xs">
        {required.map((cmd, i) => {
          const key = JSON.stringify(cmd);
          const r = byCommand.get(key);
          return (
            <li key={i} className="flex items-center gap-2 font-mono">
              {r ? (
                <Tag tone={r.ok ? "ok" : "fail"}>{r.ok ? "PASS" : `FAIL (${r.failure_class || "exit≠0"})`}</Tag>
              ) : (
                <Tag>NOT RUN</Tag>
              )}
              <span className="truncate">{key}</span>
              {r && <span className="text-faint">{fmtDuration(r.duration_ms)}</span>}
            </li>
          );
        })}
      </ul>
      {results === null && (
        <p className="text-[11px] text-faint mt-2">
          Tests execute code and run only when you click "Run tests". Missing evidence blocks
          integration unless explicitly overridden in the Go backend gates.
        </p>
      )}
    </Card>
  );
}
