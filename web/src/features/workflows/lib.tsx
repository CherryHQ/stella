import { Link } from "@tanstack/react-router";
import { ArrowLeft } from "lucide-react";
import { useI18n } from "@/lib/i18n";
import type { ComponentsProposedChild, ComponentsProposedEdge } from "@/lib/api-client/types.gen";

// The frozen plan is an opaque object on the wire (the OpenAPI `plan` field is
// untyped). These mirror the Go DTO in internal/goal/frozen.go so the detail
// page can render the tree without re-deriving the shape everywhere.
export interface FrozenPlan {
  children: FrozenNode[];
  edges?: ComponentsProposedEdge[];
}

export interface FrozenNode {
  child: ComponentsProposedChild;
  /** Present only for composite nodes; nil means "let the planner replan". */
  plan?: FrozenPlan | null;
}

/** Narrow an opaque `Workflow.plan` to the frozen-plan shape, or null if empty. */
export function asFrozenPlan(plan: unknown): FrozenPlan | null {
  if (!plan || typeof plan !== "object") return null;
  const p = plan as FrozenPlan;
  return Array.isArray(p.children) ? p : null;
}

export interface PlanStats {
  nodes: number;
  leaves: number;
  composites: number;
  /** Composite nodes with no frozen sub-plan — left to the planner at runtime. */
  semiFrozen: number;
}

/** Walk the whole frozen tree once for the headline counts. */
export function planStats(plan: FrozenPlan | null): PlanStats {
  const s: PlanStats = { nodes: 0, leaves: 0, composites: 0, semiFrozen: 0 };
  const walk = (p: FrozenPlan | null | undefined) => {
    if (!p?.children) return;
    for (const n of p.children) {
      s.nodes++;
      if (n.child.kind === "composite") {
        s.composites++;
        if (n.plan) walk(n.plan);
        else s.semiFrozen++;
      } else {
        s.leaves++;
      }
      if (n.child.kind !== "composite" && n.plan) walk(n.plan);
    }
  };
  walk(plan);
  return s;
}

/** Full-width detail page chrome for a workflow: back link, crumb, title, actions. */
export function WorkflowDetailShell({
  agentId,
  title,
  pill,
  actions,
  children,
}: {
  agentId: string;
  title: React.ReactNode;
  pill?: React.ReactNode;
  actions?: React.ReactNode;
  children: React.ReactNode;
}) {
  const { t } = useI18n();
  return (
    <div className="h-full min-h-0 overflow-y-auto bg-background">
      <div className="mx-auto max-w-[800px] px-6 py-7 pb-20 sm:px-8">
        <Link
          to="/agents/$agentId/workflows"
          params={{ agentId }}
          className="inline-flex items-center gap-1.5 text-[12.5px] font-medium text-muted-foreground hover:text-foreground"
        >
          <ArrowLeft className="size-3.5" />
          {t("workflows.title")}
        </Link>
        <div className="mt-4 font-mono text-xs font-medium text-muted-foreground">
          {t("workflows.kind")}
        </div>
        <div className="mt-1.5 flex flex-wrap items-start justify-between gap-x-4 gap-y-3">
          <h2 className="flex min-w-0 flex-wrap items-center gap-2.5 text-[22px] font-semibold tracking-tight leading-snug">
            {title}
            {pill}
          </h2>
          {actions && <div className="flex shrink-0 flex-wrap gap-2 pt-1">{actions}</div>}
        </div>
        {children}
      </div>
    </div>
  );
}
