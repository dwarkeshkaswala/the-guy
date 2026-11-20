// Agents: capability-checked adapter registry. Readiness/version/model info
// comes only from verified probes — "unknown" is shown when the adapter
// cannot prove a value (the-guy never guesses model configuration).
import { useEffect, useState } from "react";
import { useApp } from "../state/app";
import { api } from "../lib/api";
import { Button, Card, Field, Input, Modal, SectionHeader, Skeleton, Tag, Banner, CopyButton } from "../components/ui";
import type { CapabilityReport } from "../lib/types";
import { CircleCheck, CircleDashed, Pencil, RefreshCw } from "lucide-react";

export function Agents() {
  const app = useApp();
  const [reports, setReports] = useState<CapabilityReport[] | null>(null);
  const [probing, setProbing] = useState(false);
  const [editTarget, setEditTarget] = useState<CapabilityReport | null>(null);
  const [editPath, setEditPath] = useState("");
  const [saving, setSaving] = useState(false);
  const [editError, setEditError] = useState("");

  const load = async (cached: boolean) => {
    setProbing(!cached);
    try {
      setReports(await api.listAgents(cached));
    } finally {
      setProbing(false);
    }
  };

  useEffect(() => {
    load(true);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return (
    <div className="space-y-4">
      <SectionHeader
        title="Agents"
        subtitle="Local CLI adapters, verified by probing the real executables — never assumed."
        actions={
          <Button onClick={() => load(false)} loading={probing}>
            <RefreshCw size={13} /> Re-probe
          </Button>
        }
      />
      <Banner kind="info">
        the-guy never overrides model, provider, or authentication configured inside each CLI, and
        never adds permission-bypass flags. Model readiness shows <b>unknown</b> unless the adapter
        can verify it.
      </Banner>

      {reports === null ? (
        <div className="grid md:grid-cols-2 gap-3">
          <Skeleton className="h-44" />
          <Skeleton className="h-44" />
        </div>
      ) : (
        <div className="grid md:grid-cols-2 gap-3">
          {reports.map((r) => (
            <AgentCard key={r.adapter} report={r} onEdit={() => { setEditTarget(r); setEditPath(r.executable ?? ""); setEditError(""); }} />
          ))}
        </div>
      )}

      <Modal
        open={!!editTarget}
        onClose={() => setEditTarget(null)}
        title={`Executable override — ${editTarget?.display ?? ""}`}
        footer={
          <>
            {editTarget?.executable && (
              <Button
                variant="danger"
                onClick={async () => {
                  setSaving(true);
                  try {
                    await api.configureAgent(editTarget!.adapter, "");
                    app.toast("ok", "Override cleared");
                    setEditTarget(null);
                    await load(false);
                  } catch (e) {
                    app.toast("fail", String(e));
                  } finally {
                    setSaving(false);
                  }
                }}
                loading={saving}
              >
                Clear override
              </Button>
            )}
            <Button onClick={() => setEditTarget(null)}>Cancel</Button>
            <Button
              variant="primary"
              loading={saving}
              onClick={async () => {
                setSaving(true);
                setEditError("");
                try {
                  await api.configureAgent(editTarget!.adapter, editPath.trim());
                  app.toast("ok", `Override saved for '${editTarget!.adapter}' — re-probing…`);
                  setEditTarget(null);
                  await load(false);
                } catch (e) {
                  setEditError(String(e));
                } finally {
                  setSaving(false);
                }
              }}
            >
              Save override
            </Button>
          </>
        }
      >
        <div className="space-y-3.5">
          <Field
            label="Executable path"
            hint="Absolute path to the CLI executable (.cmd shims and versioned install dirs are fine). Empty = automatic discovery."
          >
            <Input value={editPath} onChange={(e) => setEditPath(e.target.value)} className="font-mono" />
          </Field>
          <div className="flex items-center gap-2 text-xs text-mut">
            <span className="font-mono">GUY_{editTarget?.adapter?.toUpperCase()}_PATH</span>
            <CopyButton text={`GUY_${editTarget?.adapter?.toUpperCase() ?? ""}_PATH=${editPath}`} />
            <span>env var equivalent</span>
          </div>
          {editError && <Banner kind="fail">{editError}</Banner>}
        </div>
      </Modal>
    </div>
  );
}

function AgentCard({ report: r, onEdit }: { report: CapabilityReport; onEdit: () => void }) {
  const verified = r.headless_verified;
  return (
    <Card className="p-4">
      <div className="flex items-start gap-3">
        {verified ? (
          <CircleCheck size={17} className="text-ok mt-0.5 shrink-0" aria-hidden />
        ) : (
          <CircleDashed size={17} className="text-warn mt-0.5 shrink-0" aria-hidden />
        )}
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2 flex-wrap">
            <span className="font-semibold">{r.display}</span>
            {r.kind === "mock" && <Tag>test/demo</Tag>}
            {verified ? <Tag tone="ok">verified headless</Tag> : <Tag tone="warn">not verifiable</Tag>}
          </div>
          <dl className="mt-2 space-y-1 text-xs">
            <Row k="Executable" v={r.executable || "not found"} mono />
            <Row k="Version" v={r.version || "unknown"} mono />
            <Row k="Source" v={r.source || "—"} mono />
            <Row k="Model / auth" v="managed by the CLI itself — unknown to the-guy" />
            <Row k="Verification" v={r.headless_note || "—"} />
            {r.extension_hint && <Row k="Note" v={`VS Code extension detected — not a CLI: ${r.extension_hint}`} />}
          </dl>
          {((r.notes ?? []).length ?? 0) > 0 && (
            <ul className="mt-2 text-[11px] text-faint space-y-0.5 list-disc pl-4">
              {(r.notes ?? []).slice(0, 3).map((n, i) => <li key={i}>{n}</li>)}
            </ul>
          )}
          {!verified && r.install_hint && (
            <p className="mt-2 text-[11px] text-mut leading-relaxed">{r.install_hint}</p>
          )}
        </div>
        <div className="shrink-0">
          <Button size="sm" onClick={onEdit}>
            <Pencil size={12} /> Override
          </Button>
        </div>
      </div>
    </Card>
  );
}

function Row({ k, v, mono }: { k: string; v: string; mono?: boolean }) {
  return (
    <div className="flex gap-3">
      <dt className="text-faint w-24 shrink-0">{k}</dt>
      <dd className={`min-w-0 break-all ${mono ? "font-mono" : ""}`}>{v}</dd>
    </div>
  );
}
