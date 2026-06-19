import { useState } from "react";
import type { TFunction } from "i18next";
import { putGoalPlan, startGoalPlanning } from "@/lib/api-client";
import type { ComponentsGoalPlan, ComponentsPlanItem } from "@/lib/api-client/types.gen";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Dialog, DialogPopup, DialogTitle } from "@/components/ui/dialog";
import { Tabs, TabsList, TabsTab, TabsPanel } from "@/components/ui/tabs";
import { SessionConversation } from "@/features/sessions/SessionConversation";
import { cn } from "@/lib/utils";

// A single-item plan is a "direct" goal (role direct, no deps); a multi-item plan
// is structured (design/impl/verify with deps). These are the only two shapes the
// backend validatePlan accepts, so the editor mirrors them: MULTI_ROLES are the
// chips shown when there is more than one step; a lone step is forced to "direct"
// on save and shows no chips.
type Role = "direct" | "design" | "impl" | "verify";
const MULTI_ROLES: Role[] = ["design", "impl", "verify"];
const ROLE_KEY = {
  design: "goals.planRoleDesign",
  impl: "goals.planRoleImpl",
  verify: "goals.planRoleVerify",
} as const;

// normRole keeps a known structured role, mapping anything else (empty, "direct",
// unknown) to "direct" so an existing single-step direct plan round-trips intact
// instead of being silently rewritten to "impl".
function normRole(r: string | undefined): Role {
  return r === "design" || r === "impl" || r === "verify" ? r : "direct";
}

// DraftItem is the structured editor's working copy of a plan item. criteria is
// held as raw textarea text (one per line) and split on save, so editing keeps
// blank/in-progress lines without dropping them.
type DraftItem = {
  id: string;
  title: string;
  role: Role;
  deps: string[];
  criteriaText: string;
};

function toDrafts(items: ComponentsPlanItem[] | undefined): DraftItem[] {
  return (items ?? []).map((it) => ({
    id: it.id,
    title: it.title,
    role: normRole(it.role),
    deps: it.deps ?? [],
    criteriaText: (it.criteria ?? []).join("\n"),
  }));
}

function chip(on: boolean) {
  return cn(
    "rounded-full px-2.5 py-0.5 text-xs font-medium transition-colors",
    on ? "bg-primary text-primary-foreground" : "bg-muted text-muted-foreground hover:bg-muted/70",
  );
}

// GoalPlanEditor edits a goal's plan two ways, both replacing the old raw-JSON
// box: "Plan with agent" continues the goal's dedicated planning session (the
// agent rewrites the plan as you chat), and "Edit steps" is a structured form
// over the same plan (add/remove steps, pick role/deps/criteria) that PUTs the
// plan. The server runs the full validation (cycles, dangling deps); the form
// only does a light shape check and surfaces server errors inline.
export function GoalPlanEditor({
  goalId,
  agentId,
  plan,
  open,
  onOpenChange,
  onSaved,
  t,
}: {
  goalId: string;
  agentId: string;
  plan: ComponentsGoalPlan | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onSaved: () => void;
  t: TFunction;
}) {
  const seedItems = () => toDrafts((plan?.pending_content ?? plan?.content)?.items);
  const [items, setItems] = useState<DraftItem[]>(seedItems);
  const [policy, setPolicy] = useState<"none" | "human">(plan?.review_policy ?? "none");
  const [tab, setTab] = useState("chat");
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [starting, setStarting] = useState(false);

  // Re-seed the form whenever an open cycle starts so reopening reflects the
  // latest server state rather than stale local edits.
  const [seed, setSeed] = useState(open);
  if (open !== seed) {
    setSeed(open);
    if (open) {
      setItems(seedItems());
      setPolicy(plan?.review_policy ?? "none");
      setTab("chat");
      setError(null);
    }
  }

  const planningSessionId = plan?.planning_session_id ?? null;

  const startPlanning = async () => {
    setStarting(true);
    setError(null);
    try {
      await startGoalPlanning({ path: { goalId }, throwOnError: true });
      // Refresh the goal/plan so planning_session_id flows back in and the chat
      // mounts; the parent re-renders this editor with the bound session.
      onSaved();
    } catch (e) {
      setError(t("goals.planChatStartFailed", { error: (e as Error).message }));
    } finally {
      setStarting(false);
    }
  };

  const setItem = (idx: number, patch: Partial<DraftItem>) =>
    setItems((prev) => prev.map((it, i) => (i === idx ? { ...it, ...patch } : it)));
  const removeItem = (idx: number) => setItems((prev) => prev.filter((_, i) => i !== idx));
  const addItem = () =>
    setItems((prev) => {
      // Crossing 1 -> many turns a "direct" plan into a structured one; the lone
      // step has no valid structured role yet, so seed it to "impl".
      const promoted =
        prev.length === 1
          ? prev.map((it) => ({
              ...it,
              role: normRole(it.role) === "direct" ? ("impl" as Role) : it.role,
            }))
          : prev;
      return [
        ...promoted,
        { id: `step${prev.length + 1}`, title: "", role: "impl", deps: [], criteriaText: "" },
      ];
    });
  const toggleDep = (idx: number, dep: string) =>
    setItem(idx, {
      deps: items[idx].deps.includes(dep)
        ? items[idx].deps.filter((d) => d !== dep)
        : [...items[idx].deps, dep],
    });

  const save = async () => {
    const cleaned = items.map((it) => ({ ...it, id: it.id.trim(), title: it.title.trim() }));
    if (cleaned.length === 0) {
      setError(t("goals.planNeedsItems"));
      return;
    }
    const ids = cleaned.map((i) => i.id);
    if (cleaned.some((i) => !i.id || !i.title) || new Set(ids).size !== ids.length) {
      setError(t("goals.planNeedsIdTitle"));
      return;
    }
    // A single step is a direct goal: force role "direct" + no deps (the only
    // shape the backend accepts for one item). Multiple steps are structured;
    // coerce any leftover "direct" to "impl" so every role is design/impl/verify.
    const single = cleaned.length === 1;
    const content = {
      items: cleaned.map((it) => ({
        id: it.id,
        title: it.title,
        role: single ? "direct" : normRole(it.role) === "direct" ? "impl" : it.role,
        deps: single ? [] : it.deps.filter((d) => ids.includes(d)),
        criteria: it.criteriaText
          .split("\n")
          .map((s) => s.trim())
          .filter(Boolean),
      })),
    };
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

        <Tabs value={tab} onValueChange={(v) => setTab(v as string)} className="mt-4">
          <TabsList>
            <TabsTab value="chat">{t("goals.planTabChat")}</TabsTab>
            <TabsTab value="edit">{t("goals.planTabEdit")}</TabsTab>
          </TabsList>

          <TabsPanel value="chat" className="mt-3">
            {planningSessionId ? (
              <SessionConversation
                agentId={agentId}
                sessionId={planningSessionId}
                inline
                bodyClassName="h-[24rem]"
                placeholder={t("goals.planChatPlaceholder")}
              />
            ) : (
              <div className="flex h-[24rem] flex-col items-center justify-center gap-3 rounded-lg border border-dashed border-border text-center">
                <p className="max-w-sm text-[12.5px] leading-relaxed text-muted-foreground">
                  {t("goals.planChatHint")}
                </p>
                <Button size="sm" loading={starting} onClick={startPlanning}>
                  {starting ? t("goals.planChatStarting") : t("goals.planChatStart")}
                </Button>
              </div>
            )}
          </TabsPanel>

          <TabsPanel value="edit" className="mt-3">
            <div className="max-h-[24rem] space-y-2.5 overflow-y-auto pr-1">
              {items.map((it, idx) => (
                <div key={idx} className="rounded-lg border border-border p-3">
                  <div className="flex items-center gap-2">
                    <Input
                      value={it.id}
                      spellCheck={false}
                      placeholder={t("goals.planItemIdPlaceholder")}
                      onChange={(e) => setItem(idx, { id: e.target.value })}
                      className="w-40 font-mono text-xs"
                    />
                    {items.length > 1 && (
                      <div className="flex gap-1">
                        {MULTI_ROLES.map((r) => (
                          <button
                            key={r}
                            type="button"
                            onClick={() => setItem(idx, { role: r })}
                            className={chip(it.role === r)}
                          >
                            {t(ROLE_KEY[r as "design" | "impl" | "verify"])}
                          </button>
                        ))}
                      </div>
                    )}
                    <Button
                      variant="ghost"
                      size="xs"
                      className="ml-auto"
                      onClick={() => removeItem(idx)}
                      aria-label={t("common.delete")}
                    >
                      ✕
                    </Button>
                  </div>
                  <Input
                    value={it.title}
                    placeholder={t("goals.planItemTitlePlaceholder")}
                    onChange={(e) => setItem(idx, { title: e.target.value })}
                    className="mt-2"
                  />
                  {items.length > 1 && (
                    <div className="mt-2 flex flex-wrap items-center gap-1.5">
                      <span className="text-xs text-muted-foreground">
                        {t("goals.planDepsLabel")}
                      </span>
                      {items
                        .map((o, j) => ({ o, j }))
                        .filter(({ o, j }) => j !== idx && o.id.trim())
                        .map(({ o }) => (
                          <button
                            key={o.id}
                            type="button"
                            onClick={() => toggleDep(idx, o.id)}
                            className={cn("font-mono", chip(it.deps.includes(o.id)))}
                          >
                            {o.id}
                          </button>
                        ))}
                    </div>
                  )}
                  <Textarea
                    value={it.criteriaText}
                    spellCheck={false}
                    placeholder={t("goals.planCriteriaPlaceholder")}
                    onChange={(e) => setItem(idx, { criteriaText: e.target.value })}
                    className="mt-2 h-16 text-[12.5px]"
                  />
                </div>
              ))}
              <Button variant="outline" size="sm" onClick={addItem} className="w-full">
                + {t("goals.planAddItem")}
              </Button>
            </div>

            <div className="mt-4 flex items-center gap-2.5">
              <span className="text-xs text-muted-foreground">
                {t("goals.planReviewPolicyLabel")}
              </span>
              {(["none", "human"] as const).map((p) => (
                <button
                  key={p}
                  type="button"
                  onClick={() => setPolicy(p)}
                  className={chip(policy === p)}
                >
                  {t(p === "none" ? "goals.planPolicyNone" : "goals.planPolicyHuman")}
                </button>
              ))}
            </div>

            <div className="mt-4 flex justify-end gap-2">
              <Button variant="ghost" size="sm" onClick={() => onOpenChange(false)}>
                {t("common.cancel")}
              </Button>
              <Button size="sm" loading={saving} onClick={save}>
                {t("common.save")}
              </Button>
            </div>
          </TabsPanel>
        </Tabs>

        {error && <p className="mt-3 text-[12.5px] text-destructive">{error}</p>}
      </DialogPopup>
    </Dialog>
  );
}
