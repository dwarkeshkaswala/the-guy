// Repositories: registration via native folder picker, live Git state,
// inspection, default selection, removal (registration only — never the
// user's directory; purge of orchestrator worktrees is a separate, explicit
// choice with a confirmation dialog).
import { useState } from "react";
import { useApp } from "../state/app";
import { api, desktop } from "../lib/api";
import {
  Button, Card, ConfirmDialog, EmptyState, Field, Input, Modal, SectionHeader,
  StatusBadge, Tag, Banner, KeyValue, CopyButton, IconButton,
} from "../components/ui";
import type { RepoView } from "../lib/types";
import { fmtDate } from "../lib/format";
import { FolderGit2, FolderOpen, Plus, RefreshCw, Search, Star, Trash2 } from "lucide-react";

export function Repos() {
  const app = useApp();
  const [query, setQuery] = useState("");
  const [addOpen, setAddOpen] = useState(false);
  const [addPath, setAddPath] = useState("");
  const [addAlias, setAddAlias] = useState("");
  const [adding, setAdding] = useState(false);
  const [addError, setAddError] = useState("");
  const [removeTarget, setRemoveTarget] = useState<RepoView | null>(null);
  const [removePurge, setRemovePurge] = useState(false);
  const [removing, setRemoving] = useState(false);
  const [inspect, setInspect] = useState<Record<string, any> | null>(null);

  const filtered = app.repos.filter(
    (r) =>
      !query ||
      r.alias.toLowerCase().includes(query.toLowerCase()) ||
      r.path.toLowerCase().includes(query.toLowerCase()),
  );

  const pickFolder = async () => {
    const dir = await desktop.pickRepositoryFolder();
    if (dir) {
      setAddPath(dir);
      if (!addAlias) setAddAlias(dir.split(/[\\/]/).filter(Boolean).pop() ?? "");
    }
  };

  const submitAdd = async () => {
    setAdding(true);
    setAddError("");
    try {
      const view = await api.addRepository(addPath.trim(), addAlias.trim());
      app.toast("ok", `Registered '${view?.alias ?? "repository"}' (${view?.default_branch})`);
      setAddOpen(false);
      setAddPath("");
      setAddAlias("");
      await app.refresh();
    } catch (e) {
      setAddError(String(e));
    } finally {
      setAdding(false);
    }
  };

  const confirmRemove = async () => {
    if (!removeTarget) return;
    setRemoving(true);
    try {
      await api.removeRepository(removeTarget.alias, removePurge);
      app.toast("ok", `Removed registration '${removeTarget.alias}'${removePurge ? " and purged clean managed worktrees" : ""}`);
      setRemoveTarget(null);
      setRemovePurge(false);
      await app.refresh();
    } catch (e) {
      app.toast("fail", String(e));
    } finally {
      setRemoving(false);
    }
  };

  return (
    <div className="space-y-4">
      <SectionHeader
        title="Repositories"
        subtitle="Any local Git repository, registered by path. State lives outside the repo."
        actions={
          <>
            <div className="relative">
              <Search size={13} className="absolute left-2.5 top-1/2 -translate-y-1/2 text-faint" aria-hidden />
              <Input
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                placeholder="Filter…"
                className="w-48 pl-7"
                aria-label="Filter repositories"
              />
            </div>
            <Button onClick={() => app.refresh()}>
              <RefreshCw size={13} /> Refresh
            </Button>
            <Button variant="primary" onClick={() => setAddOpen(true)}>
              <Plus size={13} /> Register…
            </Button>
          </>
        }
      />

      {app.repos.length === 0 ? (
        <EmptyState
          icon={<FolderGit2 size={28} />}
          title="No repositories registered"
          body="Register any local Git repository — the-guy stores its state outside the repo and never touches your default branch."
          action={
            <Button variant="primary" onClick={() => setAddOpen(true)}>
              <Plus size={13} /> Register a repository
            </Button>
          }
        />
      ) : (
        <div className="grid gap-3">
          {filtered.map((r) => (
            <Card key={r.id} className="p-4">
              <div className="flex items-start gap-3">
                <FolderGit2 size={16} className="text-mut mt-1 shrink-0" aria-hidden />
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-2 flex-wrap">
                    <span className="font-semibold">{r.alias}</span>
                    <Tag tone="mono">{r.id}</Tag>
                    {r.is_default && <Tag tone="accent">default</Tag>}
                    {r.registration_error ? (
                      <Tag tone="fail">{r.registration_error}</Tag>
                    ) : (
                      <>
                        <Tag tone="mono">{r.current_branch}</Tag>
                        {r.dirty ? (
                          <Tag tone="warn" title={r.dirty_file_count + " uncommitted change(s) — never discarded"}>dirty ({r.dirty_file_count})</Tag>
                        ) : (
                          <Tag tone="ok">clean</Tag>
                        )}
                        <Tag>{r.plans_count} plans · {r.tasks_count} tasks</Tag>
                        {r.running_tasks > 0 && <StatusBadge status="running" small />}
                        {r.needs_review > 0 && <Tag tone="warn">{r.needs_review} to review</Tag>}
                      </>
                    )}
                  </div>
                  <div className="text-xs text-faint font-mono mt-1 truncate flex items-center gap-1">
                    {r.path}
                    <CopyButton text={r.path} label="Copy path" />
                  </div>
                  <div className="text-xs text-faint mt-1">
                    default branch <span className="font-mono">{r.default_branch}</span> · added {fmtDate(r.added_at)}
                    {r.worktree_count > 0 && ` · ${r.worktree_count} git worktree(s)`}
                  </div>
                </div>
                <div className="flex items-center gap-1 shrink-0">
                  <IconButton label="Open in file manager" onClick={() => desktop.revealPath(r.path)}>
                    <FolderOpen size={14} />
                  </IconButton>
                  <IconButton
                    label={r.is_default ? "Default repository" : "Set as default"}
                    onClick={async () => {
                      await api.setDefaultRepository(r.alias);
                      await app.refresh();
                    }}
                  >
                    <Star size={14} className={r.is_default ? "text-accent-ink" : ""} />
                  </IconButton>
                  <Button
                    size="sm"
                    onClick={async () => {
                      const info = await api.inspectRepository(r.alias);
                      setInspect({ ...info, __alias: r.alias });
                    }}
                  >
                    Inspect
                  </Button>
                  <Button size="sm" variant="danger" onClick={() => setRemoveTarget(r)}>
                    <Trash2 size={13} /> Remove
                  </Button>
                </div>
              </div>
            </Card>
          ))}
        </div>
      )}

      {/* add dialog */}
      <Modal
        open={addOpen}
        onClose={() => setAddOpen(false)}
        title="Register a repository"
        footer={
          <>
            <Button onClick={() => setAddOpen(false)}>Cancel</Button>
            <Button variant="primary" onClick={submitAdd} loading={adding} disabled={!addPath.trim()}>
              Register
            </Button>
          </>
        }
      >
        <div className="space-y-3.5">
          <Field label="Repository folder" hint="Must be the root of a Git working tree. Nothing inside is modified at registration.">
            <div className="flex gap-2">
              <Input value={addPath} onChange={(e) => setAddPath(e.target.value)} placeholder="C:\path\to\repo" />
              <Button onClick={pickFolder}>Browse…</Button>
            </div>
          </Field>
          <Field label="Alias (optional)" hint="Defaults to the folder name. Used in commands and filters.">
            <Input value={addAlias} onChange={(e) => setAddAlias(e.target.value)} placeholder="my-project" />
          </Field>
          {addError && <Banner kind="fail">{addError}</Banner>}
        </div>
      </Modal>

      {/* remove confirm */}
      <ConfirmDialog
        open={!!removeTarget}
        onClose={() => setRemoveTarget(null)}
        onConfirm={confirmRemove}
        loading={removing}
        danger
        title={`Remove registration '${removeTarget?.alias ?? ""}'`}
        confirmLabel="Remove registration"
        body={
          <div className="space-y-3">
            <p>
              This removes the repository from the-guy's registry only. <b>Your Git directory is never
              deleted.</b> Task history and logs are kept in the audit trail.
            </p>
            <label className="flex items-start gap-2.5 text-sm">
              <input
                type="checkbox"
                checked={removePurge}
                onChange={(e) => setRemovePurge(e.target.checked)}
                className="mt-0.5"
              />
              <span>
                Also purge orchestrator-managed worktrees under the-guy's data directory.
                Worktrees with uncommitted changes are always refused; clean ones are removed.
              </span>
            </label>
          </div>
        }
      />

      {/* inspect drawer */}
      <Modal
        open={!!inspect}
        onClose={() => setInspect(null)}
        title={`Inspect ${inspect?.__alias ?? ""}`}
        wide
      >
        {inspect && !inspect.error && (
          <div className="space-y-4 text-sm">
            <KeyValue
              items={[
                ["Path", inspect.path],
                ["Current branch", String(inspect.current_branch ?? "—")],
                ["Default branch", String(inspect.default_branch ?? "—")],
                ["HEAD", String(inspect.head_commit ?? "—").slice(0, 12)],
                ["Dirty files", `${inspect.dirty_file_count ?? 0}${inspect.dirty ? " (never discarded)" : ""}`],
                ["Project files", (inspect.project_files ?? []).join(", ") || "—"],
                ["Test hints (not run)", (inspect.test_command_hints ?? []).join(" · ") || "—"],
                ["Agent configs", (inspect.agent_config_files ?? []).join(", ") || "—"],
              ]}
            />
            <div>
              <div className="text-xs font-medium text-mut mb-1.5">Managed worktrees</div>
              {(inspect.managed_worktrees ?? []).length === 0 ? (
                <div className="text-faint text-xs">none</div>
              ) : (
                <ul className="text-xs font-mono space-y-1">
                  {inspect.managed_worktrees.map((w: any) => (
                    <li key={w.path} className="flex items-center gap-2">
                      <Tag tone="mono">{w.branch}</Tag> {w.path}
                    </li>
                  ))}
                </ul>
              )}
            </div>
          </div>
        )}
        {inspect?.error && <Banner kind="fail">{String(inspect.error)}</Banner>}
      </Modal>
    </div>
  );
}
