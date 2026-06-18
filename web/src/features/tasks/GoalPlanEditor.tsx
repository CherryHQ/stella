import { useState } from "react";
import type { TFunction } from "i18next";
import { putGoalPlan } from "@/lib/api-client";
import type { ComponentsGoalPlan } from "@/lib/api-client/types.gen";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { Dialog, DialogPopup, DialogTitle } from "@/components/ui/dialog";
import { cn } from "@/lib/utils";

// Starter content for a goal with no plan yet — a minimal design→impl→verify DAG
// the user edits down. Mirrors the JSON the CLI's `goal plan set --file` takes,
// so the Web UI and CLI author the same structured plan.
const PLAN_TEMPLATE = `{
  "items": [
    { "id": "design", "title": "Design the approach", "role": "design", "deps": [], "criteria": [] },
    { "id": "impl", "title": "Implement", "role": "impl", "deps": ["design"], "criteria": [] },
    { "id": "verify", "title": "Verify", "role": "verify", "deps": ["impl"], "criteria": [] }
  ]
}
`;

// GoalPlanEditor stages a goal's plan content (PUT /api/goals/{id}/plan) so the
// whole deferred-goal flow — author, accept/review, materialize, activate — is
// doable from the Web UI, not just the CLI. It edits the in-flight plan when one
// exists (pending edit, else last materialized content) and seeds a template
// otherwise. The plan body is structured JSON; the server runs the full
// validation (cycles, dangling deps), so this only does a light shape check and
// surfaces server errors inline.
export function GoalPlanEditor({
  goalId,
  plan,
  open,
  onOpenChange,
  onSaved,
  t,
}: {
  goalId: string;
  plan: ComponentsGoalPlan | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onSaved: () => void;
  t: TFunction;
}) {
  const initialJson = plan
    ? JSON.stringify(plan.pending_content ?? plan.content, null, 2)
    : PLAN_TEMPLATE;
  const [json, setJson] = useState(initialJson);
  const [policy, setPolicy] = useState<"none" | "human">(plan?.review_policy ?? "none");
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  // Re-seed the form whenever a different plan/open cycle starts, so editing then
  // reopening reflects the latest server state rather than stale local edits.
  const [seed, setSeed] = useState(open);
  if (open !== seed) {
    setSeed(open);
    if (open) {
      setJson(initialJson);
      setPolicy(plan?.review_policy ?? "none");
      setError(null);
    }
  }

  const save = async () => {
    let content: { items?: unknown[] };
    try {
      content = JSON.parse(json);
    } catch (e) {
      setError(t("goals.planInvalidJson", { error: (e as Error).message }));
      return;
    }
    if (!content || !Array.isArray(content.items) || content.items.length === 0) {
      setError(t("goals.planNeedsItems"));
      return;
    }
    setSaving(true);
    setError(null);
    try {
      await putGoalPlan({
        path: { goalId },
        body: { content: content as never, review_policy: policy },
        throwOnError: true,
      });
      onSaved();
      onOpenChange(false);
    } catch (e) {
      setError(t("goals.planSaveFailed", { error: (e as Error).message }));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogPopup className="max-w-[720px]">
        <DialogTitle className="text-base font-semibold">{t("goals.planEditorTitle")}</DialogTitle>
        <p className="mt-1 text-[12.5px] leading-relaxed text-muted-foreground">
          {t("goals.planEditorHint")}
        </p>

        <div className="mt-4">
          <span className="font-mono text-xs text-muted-foreground">
            {t("goals.planJsonLabel")}
          </span>
          <Textarea
            value={json}
            onChange={(e) => setJson(e.target.value)}
            spellCheck={false}
            className="mt-1.5 h-72 font-mono text-[12.5px] leading-relaxed"
          />
        </div>

        <div className="mt-4 flex items-center gap-2.5">
          <span className="font-mono text-xs text-muted-foreground">
            {t("goals.planReviewPolicyLabel")}
          </span>
          {(["none", "human"] as const).map((p) => (
            <button
              key={p}
              type="button"
              onClick={() => setPolicy(p)}
              className={cn(
                "rounded-full px-3 py-1 text-xs font-medium transition-colors",
                policy === p
                  ? "bg-primary text-primary-foreground"
                  : "bg-muted text-muted-foreground hover:bg-muted/70",
              )}
            >
              {t(p === "none" ? "goals.planPolicyNone" : "goals.planPolicyHuman")}
            </button>
          ))}
        </div>

        {error && <p className="mt-3 text-[12.5px] text-destructive">{error}</p>}

        <div className="mt-5 flex justify-end gap-2">
          <Button variant="ghost" size="sm" onClick={() => onOpenChange(false)}>
            {t("common.cancel")}
          </Button>
          <Button size="sm" loading={saving} onClick={save}>
            {t("common.save")}
          </Button>
        </div>
      </DialogPopup>
    </Dialog>
  );
}
