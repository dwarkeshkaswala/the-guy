// Settings & diagnostics: appearance (theme/density/font/display mode),
// orchestrator settings (concurrency, timeouts, retention), doctor parity,
// paths, and redacted diagnostics export.
import { useEffect, useMemo, useState } from "react";
import { useApp } from "../state/app";
import { api } from "../lib/api";
import {
  Banner, Button, Card, Field, Input, KeyValue, SectionHeader, Select,
  Skeleton, Tag,
} from "../components/ui";
import type { DoctorReport } from "../lib/types";
import { fmtBytes } from "../lib/format";
import { Download, Monitor, Palette, SlidersHorizontal, Stethoscope } from "lucide-react";

const NUM_KEYS = [
  { key: "max_workers", label: "Max concurrent agents", hint: "Write-capable agent processes launched in parallel (1–8)." },
  { key: "run_timeout_seconds", label: "Run timeout (seconds)" },
  { key: "log_max_bytes", label: "Log cap per run (bytes)" },
  { key: "log_retention_days", label: "Log retention (days)" },
];

export function Settings() {
  const app = useApp();
  const [doctor, setDoctor] = useState<DoctorReport | null>(null);
  const [savingKey, setSavingKey] = useState("");
  const [diag, setDiag] = useState("");
  const h = app.health;
  const settings = app.settings;

  useEffect(() => {
    api.doctor("").then(setDoctor).catch(() => setDoctor(null));
  }, [app.refreshTick]);

  const save = async (key: string, value: unknown) => {
    setSavingKey(key);
    try {
      await api.updateSetting(key, value);
      await app.refresh();
      app.toast("ok", `Saved ${key}`);
    } catch (e) {
      app.toast("fail", String(e));
    } finally {
      setSavingKey("");
    }
  };

  const rendererCaps = useMemo(() => {
    // capability detection only — no claims about physical hardware
    try {
      const canvas = document.createElement("canvas");
      const gl = canvas.getContext("webgl2");
      return {
        webgl2: !!gl,
        reducedMotion: window.matchMedia("(prefers-reduced-motion: reduce)").matches,
        colorScheme: window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light",
      };
    } catch {
      return { webgl2: false, reducedMotion: false, colorScheme: "unknown" };
    }
  }, []);

  return (
    <div className="space-y-4">
      <SectionHeader title="Settings & Diagnostics" subtitle="Stored in the-guy's prefs (Go) — not browser storage." />

      <div className="grid lg:grid-cols-2 gap-4 items-start">
        {/* appearance */}
        <Card className="p-4">
          <div className="flex items-center gap-2 mb-3">
            <Palette size={14} className="text-mut" aria-hidden />
            <h3 className="font-medium text-sm">Appearance</h3>
          </div>
          <div className="grid grid-cols-2 gap-3">
            <Field label="Theme">
              <Select
                value={String(settings.ui_theme ?? "system")}
                onChange={(e) => save("ui_theme", e.target.value)}
              >
                <option value="system">System</option>
                <option value="dark">Dark</option>
                <option value="light">Light</option>
              </Select>
            </Field>
            <Field label="Density">
              <Select
                value={String(settings.ui_density ?? "comfortable")}
                onChange={(e) => save("ui_density", e.target.value)}
              >
                <option value="comfortable">Comfortable</option>
                <option value="compact">Compact</option>
              </Select>
            </Field>
            <Field label="Font size (px)">
              <Select
                value={String(settings.ui_font_size ?? 14)}
                onChange={(e) => save("ui_font_size", Number(e.target.value))}
              >
                {[12, 13, 14, 15, 16, 17, 18].map((n) => (
                  <option key={n} value={n}>{n}</option>
                ))}
              </Select>
            </Field>
            <Field label="Display mode" hint="Presentation only — never affects runs, security or approval policy.">
              <Select
                value={String(settings.display_mode ?? "auto")}
                onChange={(e) => save("display_mode", e.target.value)}
              >
                <option value="auto">Auto (detect + fallback)</option>
                <option value="performance">Performance</option>
                <option value="efficiency">Efficiency</option>
                <option value="compatibility">Compatibility</option>
              </Select>
            </Field>
          </div>
          <div className="mt-3 flex items-center gap-2 flex-wrap">
            <Monitor size={13} className="text-faint" aria-hidden />
            <Tag tone={rendererCaps.webgl2 ? "ok" : "neutral"}>WebGL2: {rendererCaps.webgl2 ? "available" : "unavailable"}</Tag>
            <Tag>reduced motion: {String(rendererCaps.reducedMotion)}</Tag>
            <Tag>OS scheme: {rendererCaps.colorScheme}</Tag>
          </div>
          <p className="text-[11px] text-faint mt-2 leading-relaxed">
            Agent output streams as read-only logs (the backend runs pipes, not PTYs), so no
            interactive terminal is offered — WebGL status is reported for transparency, not
            claimed as acceleration. Switching modes never interrupts runs.
          </p>
        </Card>

        {/* orchestrator */}
        <Card className="p-4">
          <div className="flex items-center gap-2 mb-3">
            <SlidersHorizontal size={14} className="text-mut" aria-hidden />
            <h3 className="font-medium text-sm">Orchestrator</h3>
          </div>
          <div className="grid grid-cols-2 gap-3">
            {NUM_KEYS.map((n) => (
              <NumSetting
                key={n.key}
                label={n.label}
                hint={n.hint}
                value={Number(settings[n.key] ?? 0)}
                saving={savingKey === n.key}
                onSave={(v) => save(n.key, v)}
              />
            ))}
          </div>
          <p className="text-[11px] text-faint mt-3 leading-relaxed">
            Concurrency guidance: {h ? `${h.num_cpu} logical CPUs detected` : "CPU count unknown"} — the
            default of 2 write-capable agents is deliberately conservative; worktree and
            path-collision caps are enforced in Go regardless of this setting.
          </p>
        </Card>

        {/* doctor */}
        <Card className="p-4">
          <div className="flex items-center gap-2 mb-3">
            <Stethoscope size={14} className="text-mut" aria-hidden />
            <h3 className="font-medium text-sm">Doctor</h3>
            {doctor && (
              <Tag tone={doctor.failed === 0 ? "ok" : "fail"}>
                {doctor.failed === 0 ? "all checks pass" : `${doctor.failed} failure(s)`}
              </Tag>
            )}
          </div>
          {doctor === null ? (
            <Skeleton className="h-48" />
          ) : (
            <ul className="space-y-1.5 text-xs">
              {(doctor.checks ?? []).map((c) => (
                <li key={c.name} className="flex items-start gap-2">
                  <Tag tone={c.status === "ok" ? "ok" : c.status === "warn" ? "warn" : "fail"}>
                    {c.status.toUpperCase()}
                  </Tag>
                  <div className="min-w-0">
                    <span className="font-mono">{c.name}</span>
                    <span className="text-mut"> — {c.detail}</span>
                  </div>
                </li>
              ))}
            </ul>
          )}
        </Card>

        {/* paths + diagnostics */}
        <Card className="p-4">
          <h3 className="font-medium text-sm mb-3">Paths & diagnostics</h3>
          {h && (
            <KeyValue
              items={[
                ["Version", `the-guy ${h.version} · pid ${h.pid}`],
                ["State DB", `${h.db_path} (${fmtBytes(h.db_size_bytes)})`],
                ["Data dir", h.data_dir],
                ["Prefs", h.prefs_dir],
                ["Logs", h.logs_root],
                ["Worktrees", h.worktrees_root],
                ["CPUs", `${h.num_cpu} logical / ${h.go_max_procs} Go procs`],
              ]}
            />
          )}
          <div className="mt-4 flex items-center gap-2">
            <Button
              onClick={async () => {
                const text = await api.exportDiagnostics();
                setDiag(text);
                try {
                  const blob = new Blob([text], { type: "application/json" });
                  const url = URL.createObjectURL(blob);
                  const a = document.createElement("a");
                  a.href = url;
                  a.download = "the-guy-diagnostics.json";
                  a.click();
                  URL.revokeObjectURL(url);
                  app.toast("ok", "Redacted diagnostic bundle downloaded");
                } catch {
                  app.toast("info", "Export shown below — copy it manually");
                }
              }}
            >
              <Download size={13} /> Export redacted diagnostics
            </Button>
            {diag && (
              <Button variant="ghost" onClick={() => navigator.clipboard?.writeText(diag)}>
                Copy JSON
              </Button>
            )}
          </div>
          {diag && (
            <pre className="mt-3 max-h-56 overflow-auto rounded-md bg-inset p-3 text-[11px] font-mono leading-relaxed">
              {diag}
            </pre>
          )}
          <Banner kind="info">
            The bundle contains health, doctor checks, agent reports, settings and recent events —
            with tokens, auth headers and credentials redacted. No file contents.
          </Banner>
        </Card>
      </div>
    </div>
  );
}

function NumSetting({
  label, hint, value, onSave, saving,
}: { label: string; hint?: string; value: number; onSave: (v: number) => void; saving: boolean }) {
  const [v, setV] = useState(String(value));
  useEffect(() => setV(String(value)), [value]);
  return (
    <Field label={label} hint={hint}>
      <div className="flex gap-2">
        <Input
          type="number"
          value={v}
          onChange={(e) => setV(e.target.value)}
          className="font-mono"
        />
        <Button
          size="sm"
          onClick={() => {
            const n = Number(v);
            if (Number.isFinite(n)) onSave(n);
          }}
          loading={saving}
          disabled={Number(v) === value}
        >
          Save
        </Button>
      </div>
    </Field>
  );
}
