import { CornerDownRight } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { useI18n } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import type { FrozenNode, FrozenPlan } from "@/features/workflows/lib";

/** Recursive read-only view of a frozen plan: nodes, their kind, and edges. */
export function PlanTree({ plan }: { plan: FrozenPlan }) {
  return <NodeList plan={plan} depth={0} />;
}

function NodeList({ plan, depth }: { plan: FrozenPlan; depth: number }) {
  const { t } = useI18n();
  return (
    <div className={cn("flex flex-col gap-2", depth > 0 && "mt-2 border-l border-border pl-4")}>
      {plan.children.map((n) => (
        <PlanNode key={n.child.key} node={n} depth={depth} />
      ))}
      {plan.edges?.map((e) => (
        <div
          key={`${e.upstream_key}->${e.downstream_key}`}
          className="flex items-center gap-1.5 font-mono text-xs text-muted-foreground"
        >
          <CornerDownRight className="size-3.5" />
          <span className="text-foreground">{e.upstream_key}</span>→
          <span className="text-foreground">{e.downstream_key}</span>
          <Badge size="sm" variant="secondary">
            {t(e.kind === "soft" ? "workflows.edgeSoft" : "workflows.edgeHard")}
          </Badge>
        </div>
      ))}
    </div>
  );
}

function PlanNode({ node, depth }: { node: FrozenNode; depth: number }) {
  const { t } = useI18n();
  const { child, plan } = node;
  const isComposite = child.kind === "composite";
  const semiFrozen = isComposite && !plan;
  return (
    <div className="rounded-xl border border-border bg-muted/30 px-3.5 py-2.5">
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <span className="font-mono text-xs text-muted-foreground">{child.key}</span>
        <span className="text-[13px] font-medium text-foreground">{child.title}</span>
        <Badge size="sm" variant="secondary">
          {t(isComposite ? "goals.kindComposite" : "goals.kindLeaf")}
        </Badge>
        {child.required === false && (
          <Badge size="sm" variant="outline">
            {t("workflows.optional")}
          </Badge>
        )}
        {semiFrozen && (
          <Badge size="sm" variant="outline">
            {t("workflows.semiFrozen")}
          </Badge>
        )}
      </div>
      {child.intent && (
        <p className="mt-1 text-xs leading-relaxed text-muted-foreground">{child.intent}</p>
      )}
      {plan && <NodeList plan={plan} depth={depth + 1} />}
    </div>
  );
}
