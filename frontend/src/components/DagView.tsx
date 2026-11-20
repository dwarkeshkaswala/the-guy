// Task dependency DAG over @xyflow/react. Memoized nodes; status colors map
// to the same semantic tokens as the rest of the UI. Layout is a simple
// layered topological arrangement computed client-side (stable, no animation
// for idle nodes).
import { useMemo, useCallback } from "react";
import {
  ReactFlow, Background, Controls, MiniMap, type Node, type Edge,
  type NodeProps, Handle, Position,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import type { Task } from "../lib/types";
import { statusMetaOf, shortId } from "../lib/format";
import { Eye, Lock } from "lucide-react";

const nodeTones: Record<string, { border: string; head: string }> = {
  ok: { border: "var(--ok)", head: "var(--ok-soft)" },
  fail: { border: "var(--fail)", head: "var(--fail-soft)" },
  warn: { border: "var(--warn)", head: "var(--warn-soft)" },
  running: { border: "var(--running)", head: "var(--running-soft)" },
  pending: { border: "var(--pending)", head: "var(--pending-soft)" },
  neutral: { border: "var(--border-strong)", head: "var(--neutral-soft)" },
};

type TaskNodeData = {
  task: Task;
  label: string;
};

function TaskNode({ data }: NodeProps<Node<TaskNodeData>>) {
  const t = data.task;
  const meta = statusMetaOf(t.status);
  const tone = nodeTones[meta.color] ?? nodeTones.neutral;
  return (
    <div
      className="rounded-lg border-2 bg-raised px-3 py-2 w-[220px] text-left"
      style={{ borderColor: tone.border }}
    >
      <Handle type="target" position={Position.Left} />
      <div className="flex items-center gap-1.5 text-[11px] text-faint">
        <span className="font-mono">{shortId(t.id)}</span>
        {t.read_only && (
          <span className="inline-flex items-center gap-0.5" title="read-only task">
            <Eye size={10} /> RO
          </span>
        )}
      </div>
      <div className="text-xs font-medium leading-snug mt-0.5 line-clamp-2">{t.title}</div>
      <div
        className="mt-1.5 rounded px-1.5 py-0.5 text-[11px] inline-flex items-center gap-1"
        style={{ background: tone.head, color: tone.border }}
      >
        {meta.label === "Running" && <span className="pulse-dot">●</span>}
        {meta.label}
        {t.status === "blocked" && <Lock size={10} />}
      </div>
      <div className="text-[11px] text-faint mt-1 font-mono">{t.agent}</div>
      <Handle type="source" position={Position.Right} />
    </div>
  );
}

const nodeTypes = { task: TaskNode };

export function DagView({
  tasks, onSelect,
}: { tasks: Task[]; onSelect?: (taskID: string) => void }) {
  const { nodes, edges } = useMemo(() => {
    const byId = new Map(tasks.map((t) => [t.id, t]));
    // layer = longest path from any root
    const layerOf = (t: Task, seen = new Set<string>()): number => {
      if (seen.has(t.id)) return 0; // cycle guard (Go validation prevents these)
      seen.add(t.id);
      let max = -1;
      for (const d of t.dependencies ?? []) {
        const dep = byId.get(d);
        if (dep) max = Math.max(max, layerOf(dep, seen));
      }
      return max + 1;
    };
    const layers = new Map<string, number>();
    for (const t of tasks) layers.set(t.id, layerOf(t));
    const inLayer = new Map<number, number>();
    const ns: Node<TaskNodeData>[] = tasks.map((t) => {
      const layer = layers.get(t.id) ?? 0;
      const idx = inLayer.get(layer) ?? 0;
      inLayer.set(layer, idx + 1);
      return {
        id: t.id,
        type: "task" as const,
        position: { x: layer * 280, y: idx * 130 },
        data: { task: t, label: t.title },
      };
    });
    const es: Edge[] = [];
    for (const t of tasks) {
      for (const dep of t.dependencies ?? []) {
        if (byId.has(dep)) {
          es.push({
            id: `${dep}->${t.id}`,
            source: dep,
            target: t.id,
            animated: t.status === "running" || byId.get(dep)?.status === "running",
          });
        }
      }
    }
    return { nodes: ns, edges: es };
  }, [tasks]);

  const onNodeClick = useCallback(
    (_: unknown, node: Node) => onSelect?.(node.id),
    [onSelect],
  );

  return (
    <div className="h-[420px] rounded-lg border border-edge bg-inset overflow-hidden">
      <ReactFlow
        nodes={nodes}
        edges={edges}
        nodeTypes={nodeTypes}
        onNodeClick={onNodeClick}
        fitView
        proOptions={{ hideAttribution: true }}
        minZoom={0.3}
      >
        <Background gap={20} />
        <Controls showInteractive={false} />
        <MiniMap pannable zoomable />
      </ReactFlow>
    </div>
  );
}
