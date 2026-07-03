import { useEffect, useMemo, useRef, useState } from "react";
import { useQueries, useQuery } from "@tanstack/react-query";
import type {
  ComponentsAttempt,
  ComponentsDecompositionContent,
  ComponentsEdge,
  ComponentsGoal,
  ComponentsProposedChild,
  ComponentsProposedEdge,
} from "@/lib/api-client/types.gen";
import { goalAttemptsOptions, goalChildrenOptions, goalEdgesOptions } from "@/lib/queries/goals";
import { useI18n } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  StatusPill,
  blockReasonLabel,
  displayStatus,
  goalStatusLabel,
  rollup,
  statusLabel,
} from "@/features/goals/lib";
import "@/features/goals/GoalCanvas.css";

const POLL_MS = 5_000;
const poll = (d: ComponentsGoal) => (d.lifecycle === "done" ? false : POLL_MS);

type CanvasNode =
  | {
      id: "plan" | "accept";
      kind: "plan" | "accept";
      label: string;
      title: string;
      statusLabel: string;
      meta: string;
      pseudo: true;
      active?: boolean;
      blocked?: boolean;
    }
  | {
      id: string;
      kind: "child";
      label: string;
      title: string;
      statusLabel: string;
      goal: ComponentsGoal;
      failureClass?: string;
      blockLabel?: string | null;
      active: boolean;
      blocked: boolean;
    }
  | {
      id: string;
      selectId: "plan";
      kind: "proposal";
      label: string;
      title: string;
      proposed: ComponentsProposedChild;
      pseudo: true;
    };

interface CanvasEdge {
  from: string;
  to: string;
  satisfied: boolean;
  flowing: boolean;
}

interface LayeredGraph {
  layers: CanvasNode[][];
  edges: CanvasEdge[];
  layerOf: Record<string, number>;
}

interface EdgePath {
  key: string;
  d: string;
  className: string;
  marker: string;
}

export function GoalCanvas({
  goal,
  selectedNode,
  onSelectNode,
}: {
  goal: ComponentsGoal;
  selectedNode: string | null;
  onSelectNode: (node: string) => void;
}) {
  const { t } = useI18n();
  const plan = (goal.plan ?? {}) as ComponentsDecompositionContent;
  const proposedChildren = plan.children ?? [];
  const proposedEdges = plan.edges ?? [];
  const { data: children = [] } = useQuery({
    ...goalChildrenOptions(goal.id),
    refetchInterval: poll(goal),
  });
  const childIDs = useMemo(() => new Set(children.map((child) => child.id)), [children]);
  const edgeQueries = useQueries({
    queries: children.map((child) => ({
      ...goalEdgesOptions(child.id),
      refetchInterval: poll(goal),
    })),
  });
  const attemptQueries = useQueries({
    queries: children.map((child) => ({
      ...goalAttemptsOptions(child.id),
      refetchInterval: poll(goal),
      enabled: child.lifecycle === "done" && child.done_reason === "failed",
    })),
  });
  const edges = edgeQueries
    .flatMap((query) => query.data ?? [])
    .filter((edge) => childIDs.has(edge.upstream_id));
  const attemptsByGoal = new Map(
    children.map((child, i) => [child.id, attemptQueries[i]?.data ?? []] as const),
  );
  const graph = useMemo(
    () =>
      buildGraph({
        t,
        goal,
        children,
        materializedEdges: edges,
        attemptsByGoal,
        proposedChildren,
        proposedEdges,
      }),
    [t, goal, children, edges, attemptsByGoal, proposedChildren, proposedEdges],
  );

  const wrapRef = useRef<HTMLDivElement | null>(null);
  const [paths, setPaths] = useState<EdgePath[]>([]);
  const [vertical, setVertical] = useState(false);

  useEffect(() => {
    const mq = window.matchMedia("(max-width: 1279px)");
    const update = () => setVertical(mq.matches);
    update();
    mq.addEventListener("change", update);
    return () => mq.removeEventListener("change", update);
  }, []);

  useEffect(() => {
    const wrap = wrapRef.current;
    if (!wrap) return;
    let frame = 0;
    const draw = () => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(() =>
        setPaths(edgePaths(wrap, graph.edges, graph.layerOf, vertical)),
      );
    };
    draw();
    const observer = new ResizeObserver(draw);
    observer.observe(wrap);
    for (const node of wrap.querySelectorAll("[data-goal-canvas-node]")) observer.observe(node);
    return () => {
      cancelAnimationFrame(frame);
      observer.disconnect();
    };
  }, [graph, vertical, selectedNode]);

  return (
    <div className="goal-canvas-shell flex h-full min-w-0 overflow-hidden rounded-2xl border border-border bg-card">
      <div className="goal-canvas-surface min-w-0 flex-1 overflow-auto p-6 xl:p-10">
        <div ref={wrapRef} className="goal-canvas-wrap w-max max-w-none">
          <svg className="goal-canvas-edges" aria-hidden="true">
            <defs>
              <marker
                id="goal-canvas-arrow"
                viewBox="0 0 8 8"
                refX="7"
                refY="4"
                markerWidth="7"
                markerHeight="7"
                orient="auto-start-reverse"
              >
                <path className="marker-default" d="M 0 0 L 8 4 L 0 8 z" />
              </marker>
              <marker
                id="goal-canvas-arrow-satisfied"
                viewBox="0 0 8 8"
                refX="7"
                refY="4"
                markerWidth="7"
                markerHeight="7"
                orient="auto-start-reverse"
              >
                <path className="marker-satisfied" d="M 0 0 L 8 4 L 0 8 z" />
              </marker>
              <marker
                id="goal-canvas-arrow-flowing"
                viewBox="0 0 8 8"
                refX="7"
                refY="4"
                markerWidth="7"
                markerHeight="7"
                orient="auto-start-reverse"
              >
                <path className="marker-flowing" d="M 0 0 L 8 4 L 0 8 z" />
              </marker>
            </defs>
            {paths.map((path) => (
              <path key={path.key} d={path.d} className={path.className} markerEnd={path.marker} />
            ))}
          </svg>
          <div className="goal-canvas-flow">
            {graph.layers.map((layer, index) => (
              <div key={index} className="goal-canvas-layer">
                {layer.map((node) => (
                  <CanvasNodeCard
                    key={node.id}
                    node={node}
                    selected={selectedNode === selectableNodeID(node)}
                    onSelect={() => onSelectNode(selectableNodeID(node))}
                  />
                ))}
              </div>
            ))}
          </div>
        </div>
      </div>
    </div>
  );
}

function CanvasNodeCard({
  node,
  selected,
  onSelect,
}: {
  node: CanvasNode;
  selected: boolean;
  onSelect: () => void;
}) {
  const { t } = useI18n();
  const isChild = node.kind === "child";
  const isProposal = node.kind === "proposal";
  return (
    <Button
      type="button"
      variant="outline"
      data-goal-canvas-node
      data-node-id={node.id}
      className={cn(
        "goal-canvas-node h-auto flex-col items-stretch justify-start gap-0 p-3",
        (node.kind === "plan" || node.kind === "accept") && "is-pseudo",
        isProposal && "is-ghost opacity-80",
        isChild && node.active && "is-running",
        isChild && node.blocked && "is-blocked",
        selected && "is-selected",
      )}
      onClick={onSelect}
    >
      <span className="flex items-center justify-between gap-2">
        <span className="font-mono text-[10px] uppercase tracking-[0.1em] text-muted-foreground">
          {node.label}
        </span>
        {isChild || node.kind === "plan" || node.kind === "accept" ? (
          <StatusPill
            status={
              isChild
                ? displayStatus(node.goal)
                : node.kind === "accept" && node.statusLabel === t("goals.statusAccepted")
                  ? "accepted"
                  : "draft"
            }
            label={node.statusLabel}
          />
        ) : (
          <Badge variant="outline" size="sm">
            {t("goals.proposedChild")}
          </Badge>
        )}
      </span>
      <span className="goal-canvas-node-title mt-2 text-sm font-medium leading-snug">
        {node.title}
      </span>
      {isChild ? (
        <span className="mt-2 flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
          <span>{t("goals.attemptCountShort", { count: node.goal.attempt_count ?? 0 })}</span>
          {node.failureClass && (
            <Badge variant="destructive" size="sm">
              {node.failureClass}
            </Badge>
          )}
          {node.blockLabel && (
            <Badge variant="warning" size="sm">
              {node.blockLabel}
            </Badge>
          )}
        </span>
      ) : isProposal ? (
        <span className="mt-2 text-xs text-muted-foreground">
          {t(node.proposed.kind === "composite" ? "goals.kindComposite" : "goals.kindLeaf")}
        </span>
      ) : node.meta ? (
        <span className="mt-2 text-xs text-muted-foreground">{node.meta}</span>
      ) : null}
    </Button>
  );
}

function buildGraph({
  t,
  goal,
  children,
  materializedEdges,
  attemptsByGoal,
  proposedChildren,
  proposedEdges,
}: {
  t: ReturnType<typeof useI18n>["t"];
  goal: ComponentsGoal;
  children: ComponentsGoal[];
  materializedEdges: ComponentsEdge[];
  attemptsByGoal: Map<string, ComponentsAttempt[]>;
  proposedChildren: ComponentsProposedChild[];
  proposedEdges: ComponentsProposedEdge[];
}): LayeredGraph {
  if (children.length > 0) {
    const sorted = [...children].sort(
      (a, b) => (a.position ?? 0) - (b.position ?? 0) || a.created_at.localeCompare(b.created_at),
    );
    const childByID = new Map(sorted.map((child) => [child.id, child]));
    const incoming = new Map<string, ComponentsEdge[]>();
    const outgoing = new Map<string, ComponentsEdge[]>();
    for (const edge of materializedEdges) {
      incoming.set(edge.goal_id, [...(incoming.get(edge.goal_id) ?? []), edge]);
      outgoing.set(edge.upstream_id, [...(outgoing.get(edge.upstream_id) ?? []), edge]);
    }
    const nodes: CanvasNode[] = [
      {
        id: "plan",
        kind: "plan",
        label: t("goals.canvasNodePlanKind"),
        title: t("goals.canvasNodePlanTitle"),
        statusLabel: goal.planned_at
          ? t("goals.canvasPlanMaterialized")
          : statusLabel(t, displayStatus(goal)),
        meta: t("goals.canvasPlanMeta", {
          children: sorted.length,
          edges: materializedEdges.length,
        }),
        pseudo: true,
      },
      ...sorted.map((child) => ({
        id: child.id,
        kind: "child" as const,
        label: t(child.kind === "composite" ? "goals.kindComposite" : "goals.kindLeaf"),
        title: child.title,
        statusLabel: goalStatusLabel(t, child),
        goal: child,
        failureClass: latestFailureClass(attemptsByGoal.get(child.id) ?? []),
        blockLabel: blockReasonLabel(t, child),
        active: child.lifecycle === "active",
        blocked: child.lifecycle === "blocked",
      })),
      {
        id: "accept",
        kind: "accept",
        label: t("goals.canvasNodeAcceptKind"),
        title: t("goals.canvasNodeAcceptTitle"),
        statusLabel:
          goal.done_reason === "accepted"
            ? t("goals.statusAccepted")
            : t("goals.canvasAcceptPending"),
        meta: t("goals.requiredOf", {
          accepted: rollup(sorted).accepted,
          total: rollup(sorted).total,
        }),
        pseudo: true,
      },
    ];
    const edges: CanvasEdge[] = [];
    for (const child of sorted) {
      if ((incoming.get(child.id) ?? []).length === 0)
        edges.push({
          from: "plan",
          to: child.id,
          satisfied: true,
          flowing: child.lifecycle === "active",
        });
    }
    for (const edge of materializedEdges) {
      const upstream = childByID.get(edge.upstream_id);
      const downstream = childByID.get(edge.goal_id);
      if (!upstream || !downstream) continue;
      edges.push({
        from: edge.upstream_id,
        to: edge.goal_id,
        satisfied: upstream.lifecycle === "done" && upstream.done_reason === "accepted",
        flowing: downstream.lifecycle === "active",
      });
    }
    for (const child of sorted) {
      if ((outgoing.get(child.id) ?? []).length === 0) {
        edges.push({
          from: child.id,
          to: "accept",
          satisfied: child.lifecycle === "done" && child.done_reason === "accepted",
          flowing:
            goal.lifecycle === "active" &&
            child.lifecycle === "done" &&
            child.done_reason === "accepted",
        });
      }
    }
    return layerGraph(nodes, edges);
  }

  const nodes: CanvasNode[] = [
    {
      id: "plan",
      kind: "plan",
      label: t("goals.canvasNodePlanKind"),
      title: t("goals.canvasNodePlanTitle"),
      statusLabel: goalStatusLabel(t, goal),
      meta: proposedChildren.length
        ? t("goals.canvasPlanMeta", {
            children: proposedChildren.length,
            edges: proposedEdges.length,
          })
        : "",
      pseudo: true,
    },
    ...proposedChildren.map((child) => ({
      id: `proposal:${child.key}`,
      selectId: "plan" as const,
      kind: "proposal" as const,
      label: t(child.kind === "composite" ? "goals.kindComposite" : "goals.kindLeaf"),
      title: child.title,
      proposed: child,
      pseudo: true as const,
    })),
    {
      id: "accept",
      kind: "accept",
      label: t("goals.canvasNodeAcceptKind"),
      title: t("goals.canvasNodeAcceptTitle"),
      statusLabel: t("goals.canvasAcceptPending"),
      meta: "",
      pseudo: true,
    },
  ];
  const byKey = new Map(proposedChildren.map((child) => [child.key, `proposal:${child.key}`]));
  const incoming = new Set<string>();
  const outgoing = new Set<string>();
  const edges: CanvasEdge[] = [];
  for (const edge of proposedEdges) {
    const from = byKey.get(edge.upstream_key);
    const to = byKey.get(edge.downstream_key);
    if (!from || !to) continue;
    incoming.add(to);
    outgoing.add(from);
    edges.push({ from, to, satisfied: false, flowing: false });
  }
  for (const id of byKey.values()) {
    if (!incoming.has(id)) edges.push({ from: "plan", to: id, satisfied: false, flowing: false });
    if (!outgoing.has(id)) edges.push({ from: id, to: "accept", satisfied: false, flowing: false });
  }
  if (proposedChildren.length === 0)
    edges.push({ from: "plan", to: "accept", satisfied: false, flowing: false });
  return layerGraph(nodes, edges);
}

function layerGraph(nodes: CanvasNode[], edges: CanvasEdge[]): LayeredGraph {
  const layerOf: Record<string, number> = Object.fromEntries(nodes.map((node) => [node.id, 0]));
  let changed = true;
  let passes = 0;
  const maxPasses = Math.max(1, nodes.length * Math.max(1, edges.length));
  while (changed && passes < maxPasses) {
    changed = false;
    passes++;
    for (const edge of edges) {
      if (layerOf[edge.to] < layerOf[edge.from] + 1) {
        layerOf[edge.to] = layerOf[edge.from] + 1;
        changed = true;
      }
    }
  }
  const originalOrder = new Map(nodes.map((node, index) => [node.id, index]));
  const upstream = new Map<string, string[]>();
  for (const edge of edges) upstream.set(edge.to, [...(upstream.get(edge.to) ?? []), edge.from]);
  const maxLayer = Math.max(...Object.values(layerOf), 0);
  const layers: CanvasNode[][] = [];
  for (let layer = 0; layer <= maxLayer; layer++) {
    layers[layer] = nodes.filter((node) => layerOf[node.id] === layer);
  }
  const orderOf = new Map<string, number>();
  for (const layer of layers) {
    layer.forEach((node, index) => orderOf.set(node.id, index));
  }
  for (let layer = 1; layer < layers.length; layer++) {
    layers[layer].sort((a, b) => {
      const baryA = barycenter(upstream.get(a.id) ?? [], orderOf, originalOrder);
      const baryB = barycenter(upstream.get(b.id) ?? [], orderOf, originalOrder);
      return baryA - baryB || (originalOrder.get(a.id) ?? 0) - (originalOrder.get(b.id) ?? 0);
    });
    layers[layer].forEach((node, index) => orderOf.set(node.id, index));
  }
  return { layers, edges, layerOf };
}

function barycenter(
  ids: string[],
  orderOf: Map<string, number>,
  originalOrder: Map<string, number>,
) {
  if (ids.length === 0) return Number.POSITIVE_INFINITY;
  return (
    ids.reduce((sum, id) => sum + (orderOf.get(id) ?? originalOrder.get(id) ?? 0), 0) / ids.length
  );
}

function edgePaths(
  wrap: HTMLDivElement,
  edges: CanvasEdge[],
  layerOf: Record<string, number>,
  vertical: boolean,
): EdgePath[] {
  const wrapRect = wrap.getBoundingClientRect();
  return edges.flatMap((edge) => {
    const source = wrap
      .querySelector(`[data-node-id="${cssEscape(edge.from)}"]`)
      ?.getBoundingClientRect();
    const target = wrap
      .querySelector(`[data-node-id="${cssEscape(edge.to)}"]`)
      ?.getBoundingClientRect();
    if (!source || !target) return [];
    const skip = Math.abs((layerOf[edge.to] ?? 0) - (layerOf[edge.from] ?? 0)) > 1;
    const d = vertical
      ? verticalPath(source, target, wrapRect, skip)
      : horizontalPath(source, target, wrapRect, skip);
    const className = edge.flowing ? "is-flowing" : edge.satisfied ? "is-satisfied" : "";
    const marker = edge.flowing
      ? "url(#goal-canvas-arrow-flowing)"
      : edge.satisfied
        ? "url(#goal-canvas-arrow-satisfied)"
        : "url(#goal-canvas-arrow)";
    return [{ key: `${edge.from}->${edge.to}`, d, className, marker }];
  });
}

function horizontalPath(source: DOMRect, target: DOMRect, wrap: DOMRect, skip: boolean) {
  const x1 = source.right - wrap.left;
  const y1 = source.top + source.height / 2 - wrap.top;
  const x2 = target.left - wrap.left;
  const y2 = target.top + target.height / 2 - wrap.top;
  if (skip) {
    const dip = Math.max(y1, y2) + 64;
    return `M ${x1} ${y1} C ${x1 + 70} ${dip}, ${x2 - 70} ${dip}, ${x2} ${y2}`;
  }
  const mx = (x1 + x2) / 2;
  return `M ${x1} ${y1} C ${mx} ${y1}, ${mx} ${y2}, ${x2} ${y2}`;
}

function verticalPath(source: DOMRect, target: DOMRect, wrap: DOMRect, skip: boolean) {
  const x1 = source.left + source.width / 2 - wrap.left;
  const y1 = source.bottom - wrap.top;
  const x2 = target.left + target.width / 2 - wrap.left;
  const y2 = target.top - wrap.top;
  if (skip) {
    const bow = Math.max(x1, x2) + 56;
    return `M ${x1} ${y1} C ${bow} ${y1 + 40}, ${bow} ${y2 - 40}, ${x2} ${y2}`;
  }
  const my = (y1 + y2) / 2;
  return `M ${x1} ${y1} C ${x1} ${my}, ${x2} ${my}, ${x2} ${y2}`;
}

function selectableNodeID(node: CanvasNode): string {
  return "selectId" in node ? node.selectId : node.id;
}

function latestFailureClass(attempts: ComponentsAttempt[]): string | undefined {
  return [...attempts]
    .sort((a, b) => b.attempt_no - a.attempt_no)
    .find((attempt) => attempt.failure_class)?.failure_class;
}

function cssEscape(value: string) {
  return typeof CSS !== "undefined" && CSS.escape ? CSS.escape(value) : value.replace(/"/g, '\\"');
}
