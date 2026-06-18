import { useMemo } from "react";
import type { TFunction } from "i18next";
import type { ComponentsGoalPlan, ComponentsPlanItem } from "@/lib/api-client/types.gen";
import type { MessageKey } from "@/lib/i18n/messages";

// PlanSection renders a goal's plan (the pending edit if any, else the last
// materialized content) as a read-only list of items with role, dependencies,
// and acceptance criteria. Shared by the goal detail page and the DAG dialog.
export function PlanSection({ plan, t }: { plan: ComponentsGoalPlan; t: TFunction }) {
  const content = plan.pending_content ?? plan.content;
  const items = content?.items ?? [];
  const titleOf = useMemo(() => {
    const byId = new Map(items.map((it) => [it.id, it.title]));
    return (id: string) => byId.get(id) ?? id;
  }, [items]);

  if (items.length === 0) return null;

  return (
    <div className="mb-6">
      <div className="mb-3 flex items-center gap-2.5">
        <span className="font-mono text-xs font-semibold text-muted-foreground">
          {t("goals.planTitle")}
        </span>
        <span className="rounded-full bg-muted px-2 py-0.5 font-mono text-[10.5px] text-muted-foreground">
          {t(`goals.planStatus.${plan.status}` as MessageKey)}
        </span>
        {plan.pending_content && (
          <span className="rounded-full bg-chart-4/15 px-2 py-0.5 font-mono text-[10.5px] text-chart-4">
            {t("goals.planPending")}
          </span>
        )}
        <span className="h-px flex-1 bg-border" />
      </div>

      <ol className="space-y-2">
        {items.map((item, i) => (
          <PlanItemRow key={item.id} index={i} item={item} titleOf={titleOf} t={t} />
        ))}
      </ol>
    </div>
  );
}

function PlanItemRow({
  index,
  item,
  titleOf,
  t,
}: {
  index: number;
  item: ComponentsPlanItem;
  titleOf: (id: string) => string;
  t: TFunction;
}) {
  const role = item.role ?? "direct";
  return (
    <li className="rounded-xl border border-border bg-card p-3.5">
      <div className="flex items-center gap-2.5">
        <span className="font-mono text-xs text-muted-foreground">{index + 1}</span>
        <span className="flex-1 truncate text-[13px] font-medium">{item.title}</span>
        <span className="rounded-full bg-muted px-2 py-0.5 font-mono text-[10.5px] capitalize text-muted-foreground">
          {t(`goals.planRole.${role}` as MessageKey)}
        </span>
      </div>

      {item.deps && item.deps.length > 0 && (
        <div className="mt-2 flex flex-wrap items-center gap-1.5 text-[11.5px] text-muted-foreground">
          <span className="font-mono">{t("goals.planDeps")}:</span>
          {item.deps.map((d) => (
            <span key={d} className="rounded-md bg-muted px-1.5 py-0.5">
              {titleOf(d)}
            </span>
          ))}
        </div>
      )}

      {item.criteria && item.criteria.length > 0 && (
        <div className="mt-2">
          <span className="font-mono text-[11.5px] text-muted-foreground">
            {t("goals.planCriteria")}
          </span>
          <ul className="mt-1 space-y-0.5">
            {item.criteria.map((c, i) => (
              <li key={i} className="text-[12px] text-foreground">
                • {c}
              </li>
            ))}
          </ul>
        </div>
      )}
    </li>
  );
}
