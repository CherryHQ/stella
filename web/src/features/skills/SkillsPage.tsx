import { useCallback, useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  deleteScopedSkill as deleteScopedSkillRequest,
  getScopedSkill,
  getScopedSkillFile,
  installScopedSkill,
  listAgents,
  listScopedSkills,
  updateScopedSkill,
  uploadScopedSkill,
} from "@/lib/api-client/sdk.gen";
import { formatTime } from "@/lib/time";
import type { Agent, Skill } from "@/lib/types";
import {
  AlertDialog,
  AlertDialogClose,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogPopup,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipPopup, TooltipTrigger } from "@/components/ui/tooltip";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Spinner } from "@/components/ui/spinner";
import {
  Select,
  SelectItem,
  SelectPopup,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useI18n } from "@/lib/i18n";
import { SCOPE_DESC_KEY, SCOPE_LABEL_KEY, isSkillReadOnly } from "@/lib/skill-scope";
import { apiErrorMessage, apiErrorStatus } from "@/lib/api-error";
import { useToast } from "@/hooks/use-toast";
import { meQueryOptions } from "@/lib/queries/me";
import type { ScopedSkillScope } from "@/lib/queries/skills";
import { SkillFilePreview } from "@/features/sessions/SkillFilePreview";
import {
  SettingsDetailSheet,
  SettingsGridPage,
  SettingsList,
  SettingsRow,
  SettingsSection,
} from "@/features/settings/SettingsCardGrid";
import { DetailPanel, DetailPanelHeader } from "@/features/settings/SettingsDetailPanel";
import { Lock, Plus, Puzzle } from "lucide-react";

type ScopeOwner = "me" | "global";
type ScopeRange = "all" | "specific";
type AddMode = "install" | "upload";

function isAgentScope(scope: ScopedSkillScope) {
  return scope === "user_agent" || scope === "system_agent";
}

function toScope(owner: ScopeOwner, range: ScopeRange): ScopedSkillScope {
  if (range === "specific") return owner === "global" ? "system_agent" : "user_agent";
  return owner === "global" ? "system" : "user";
}

// One hue per scope, drawn from the chart palette tokens: a scope is a category,
// which is what `chart-*` means. Reused by the list group rails, the row icon and
// the precedence ladder so a scope reads as the same color everywhere.
//
// There is deliberately no `text` entry. These tokens are tuned to be plotted as
// areas, and as words they run 2.4-3.8:1 — `chart-4` as a scope label measured
// 2.35:1. The dot carries the hue; the label is read, so it stays on
// `--foreground` and the active row is marked by weight and its own tint.
const SCOPE_COLOR: Record<ScopedSkillScope, { dot: string; soft: string }> = {
  user: { dot: "bg-chart-2", soft: "bg-chart-2/12" },
  user_agent: { dot: "bg-chart-1", soft: "bg-chart-1/12" },
  system: { dot: "bg-chart-4", soft: "bg-chart-4/12" },
  system_agent: { dot: "bg-chart-5", soft: "bg-chart-5/12" },
};

// Render order for the grouped skill list.
const SCOPE_ORDER: ScopedSkillScope[] = ["user", "user_agent", "system", "system_agent"];

// Resolution precedence, highest first: a more specific scope overrides a less
// specific one at runtime. Drives the precedence ladder so the override chain is
// visible.
const SCOPE_PRIORITY: ScopedSkillScope[] = ["user_agent", "user", "system_agent", "system"];

export function SkillsPage() {
  const { t } = useI18n();
  const { data: me } = useQuery(meQueryOptions);
  const isAdmin = me?.is_admin ?? false;

  const [skills, setSkills] = useState<Skill[]>([]);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [agents, setAgents] = useState<Agent[]>([]);

  // Add-form state, independent of the list (which shows every visible scope).
  const [addMode, setAddMode] = useState<AddMode>("install");
  const [formOwner, setFormOwner] = useState<ScopeOwner>("me");
  const [formRange, setFormRange] = useState<ScopeRange>("all");
  const [formAgentID, setFormAgentID] = useState("");
  const [source, setSource] = useState("");
  const [uploadFile, setUploadFile] = useState<File | null>(null);
  const [addSheetOpen, setAddSheetOpen] = useState(false);

  // Detail sheet (read-only skill view with file list).
  const [detailSkill, setDetailSkill] = useState<Skill | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [detailFile, setDetailFile] = useState("SKILL.md");
  const [detailFileContent, setDetailFileContent] = useState("");
  const [detailFileEncoding, setDetailFileEncoding] = useState<string | undefined>(undefined);
  const [detailFileLoading, setDetailFileLoading] = useState(false);

  // One confirm dialog serves the whole list: a row menu item only has to name
  // its target, so there is no need for a dialog instance per row.
  const [pendingDelete, setPendingDelete] = useState<Skill | null>(null);

  const { showToast } = useToast();

  const openAddSheet = useCallback(() => {
    setAddMode("install");
    setSource("");
    setUploadFile(null);
    setFormOwner("me");
    setFormRange("all");
    setFormAgentID("");
    setAddSheetOpen(true);
  }, []);

  // Fetch every scope the caller can see and merge into one flat list. Agent
  // scopes are keyed per-agent, so they need one query per agent; the page loads
  // once, so the fan-out stays bounded. Empty/failed scopes contribute nothing.
  const fetchScope = useCallback(async (scope: ScopedSkillScope, agentID?: string) => {
    try {
      const { data } = await listScopedSkills({
        query: { scope, agent_id: agentID },
        throwOnError: true,
      });
      return (data?.skills as Skill[]) ?? [];
    } catch {
      return [];
    }
  }, []);

  const loadSkills = useCallback(
    async (agentList: Agent[]) => {
      setLoading(true);
      try {
        const jobs: Promise<Skill[]>[] = [fetchScope("user")];
        if (isAdmin) jobs.push(fetchScope("system"));
        for (const agent of agentList) {
          jobs.push(fetchScope("user_agent", agent.id));
          if (isAdmin) jobs.push(fetchScope("system_agent", agent.id));
        }
        const results = await Promise.all(jobs);
        setSkills(results.flat());
      } finally {
        setLoading(false);
      }
    },
    [isAdmin, fetchScope],
  );

  // Refetch a single scope (plus agent, for agent-keyed scopes) and splice it
  // back into the flat list. A mutation only changes one slice, so this avoids
  // the full 2N+2 fan-out of loadSkills on every install/delete.
  const reloadScope = useCallback(
    async (scope: ScopedSkillScope, agentID?: string) => {
      const fetched = await fetchScope(scope, agentID);
      setSkills((prev) => [
        ...prev.filter((s) => !(s.scope === scope && (agentID ? s.agent_id === agentID : true))),
        ...fetched,
      ]);
    },
    [fetchScope],
  );

  const loadAgents = useCallback(async () => {
    try {
      const { data } = await listAgents({ query: { include_all: true }, throwOnError: true });
      const list = (data?.agents as Agent[]) ?? [];
      setAgents(list);
      return list;
    } catch {
      setAgents([]);
      return [];
    }
  }, []);

  useEffect(() => {
    const init = async () => {
      const agentList = await loadAgents();
      await loadSkills(agentList);
    };
    void init();
  }, [loadAgents, loadSkills]);

  const installSkill = useCallback(async () => {
    const agentScoped = formRange === "specific";
    if (agentScoped && !formAgentID) {
      showToast(t("skills.scope.agentMissing"), "error");
      return;
    }
    const scope = toScope(formOwner, formRange);
    setSaving(true);
    try {
      if (addMode === "upload") {
        if (!uploadFile) {
          showToast(t("skills.fileRequired"), "error");
          return;
        }
        await uploadScopedSkill({
          body: { file: uploadFile, scope, agent_id: agentScoped ? formAgentID : undefined },
          throwOnError: true,
        });
      } else {
        if (!source.trim()) {
          showToast(t("skills.sourceRequired"), "error");
          return;
        }
        await installScopedSkill({
          body: { source: source.trim(), scope, agent_id: agentScoped ? formAgentID : undefined },
          throwOnError: true,
        });
      }
      showToast(t("skills.installed"));
      setSource("");
      setUploadFile(null);
      setAddSheetOpen(false);
      await reloadScope(scope, agentScoped ? formAgentID : undefined);
    } catch (e) {
      showToast(apiErrorMessage(e, t("skills.installFailed")), "error");
    } finally {
      setSaving(false);
    }
  }, [addMode, source, uploadFile, formOwner, formRange, formAgentID, showToast, reloadScope, t]);

  const toggleModelInvocation = useCallback(
    async (skill: Skill) => {
      if (!skill.content_digest) return showToast(t("skills.digestUnavailable"), "error");
      try {
        await updateScopedSkill({
          path: { id: skill.id },
          body: {
            disable_model_invocation: !skill.disable_model_invocation,
            expected_digest: skill.content_digest,
          },
          throwOnError: true,
        });
        await reloadScope(
          skill.scope as ScopedSkillScope,
          isAgentScope(skill.scope as ScopedSkillScope) ? (skill.agent_id ?? undefined) : undefined,
        );
      } catch (e) {
        if (apiErrorStatus(e) === 409) {
          showToast(t("skills.conflict"), "error");
          await reloadScope(
            skill.scope as ScopedSkillScope,
            isAgentScope(skill.scope as ScopedSkillScope)
              ? (skill.agent_id ?? undefined)
              : undefined,
          );
          return;
        }
        showToast(apiErrorMessage(e, t("skills.updateFailed")), "error");
      }
    },
    [reloadScope, showToast, t],
  );

  const deleteSkill = useCallback(
    async (skill: Skill) => {
      if (!window.confirm(t("skills.deleteConfirm", { name: skill.name }))) return;
      if (!skill.content_digest) return showToast(t("skills.digestUnavailable"), "error");
      try {
        await deleteScopedSkillRequest({
          path: { id: skill.id },
          query: { expected_digest: skill.content_digest },
          throwOnError: true,
        });
        showToast(t("skills.deleted"));
        if (detailSkill?.id === skill.id) setDetailSkill(null);
        await reloadScope(
          skill.scope as ScopedSkillScope,
          isAgentScope(skill.scope as ScopedSkillScope) ? (skill.agent_id ?? undefined) : undefined,
        );
      } catch (e) {
        if (apiErrorStatus(e) === 409) {
          showToast(t("skills.conflict"), "error");
          await reloadScope(
            skill.scope as ScopedSkillScope,
            isAgentScope(skill.scope as ScopedSkillScope)
              ? (skill.agent_id ?? undefined)
              : undefined,
          );
          return;
        }
        showToast(apiErrorMessage(e, t("skills.deleteFailed")), "error");
      }
    },
    [detailSkill, reloadScope, showToast, t],
  );

  const openDetailFile = useCallback(async (skillID: string, path: string) => {
    setDetailFile(path);
    setDetailFileLoading(true);
    try {
      const { data } = await getScopedSkillFile({
        path: { id: skillID },
        query: { path },
        throwOnError: true,
      });
      setDetailFileContent(data?.content ?? "");
      setDetailFileEncoding(data?.encoding);
    } catch {
      setDetailFileContent("");
      setDetailFileEncoding(undefined);
    } finally {
      setDetailFileLoading(false);
    }
  }, []);

  const openDetail = useCallback(
    async (skill: Skill) => {
      setDetailSkill(skill);
      setDetailLoading(true);
      try {
        const { data } = await getScopedSkill({ path: { id: skill.id }, throwOnError: true });
        const full = (data as Skill) ?? skill;
        setDetailSkill(full);
        await openDetailFile(full.id, full.files?.[0] ?? "SKILL.md");
      } catch {
        await openDetailFile(skill.id, "SKILL.md");
      } finally {
        setDetailLoading(false);
      }
    },
    [openDetailFile],
  );

  const agentName = (id?: string | null) =>
    (id && agents.find((a) => a.id === id)?.name) || id || "";
  const skillGroups = SCOPE_ORDER.map((scope) => ({
    scope,
    items: skills.filter((s) => s.scope === scope),
  })).filter((g) => g.items.length > 0);
  const formScope = toScope(formOwner, formRange);

  const selectScope = (scope: ScopedSkillScope) => {
    setFormOwner(scope === "system" || scope === "system_agent" ? "global" : "me");
    setFormRange(scope === "user_agent" || scope === "system_agent" ? "specific" : "all");
  };

  const addPanel = (
    <DetailPanel>
      <DetailPanelHeader title={t("skills.addTitle")} />

      {/* The precedence ladder IS the scope picker: each row is selectable and
          its position shows where the skill lands in the runtime override order. */}
      <div className="space-y-3">
        <p className="text-xs font-medium text-muted-foreground">
          {t("skills.scope.priorityTitle")}
        </p>
        <ul className="space-y-1">
          {SCOPE_PRIORITY.filter(
            (scope) => isAdmin || (scope !== "system" && scope !== "system_agent"),
          ).map((scope) => {
            const active = scope === formScope;
            return (
              <li key={scope}>
                <button
                  type="button"
                  onClick={() => selectScope(scope)}
                  className={`flex w-full cursor-pointer items-center gap-2.5 rounded-md px-3 py-2 text-left text-sm transition-colors ${
                    active ? SCOPE_COLOR[scope].soft : "hover:bg-muted/60"
                  }`}
                >
                  <span className={`size-2.5 shrink-0 rounded-full ${SCOPE_COLOR[scope].dot}`} />
                  <span className={active ? "font-semibold text-foreground" : "text-foreground"}>
                    {t(SCOPE_LABEL_KEY[scope])}
                  </span>
                  {active && (
                    <span className="ml-auto text-xs font-medium text-muted-foreground">
                      {t("skills.scope.current")}
                    </span>
                  )}
                </button>
              </li>
            );
          })}
        </ul>

        <p className="px-1 text-xs text-muted-foreground">{t(SCOPE_DESC_KEY[formScope])}</p>

        {formRange === "specific" && (
          <Select
            value={formAgentID || null}
            onValueChange={(value) => setFormAgentID((value as string | null) ?? "")}
          >
            <SelectTrigger>
              <SelectValue placeholder={t("skills.scope.selectAgent")}>
                {(value) =>
                  value ? agents.find((agent) => agent.id === value)?.name || value : null
                }
              </SelectValue>
            </SelectTrigger>
            <SelectPopup>
              {agents.map((agent) => (
                <SelectItem key={agent.id} value={agent.id}>
                  {agent.name || agent.id}
                </SelectItem>
              ))}
            </SelectPopup>
          </Select>
        )}
      </div>

      <div className="space-y-3 border-t border-border pt-4">
        <div className="flex items-center gap-1.5">
          {(["install", "upload"] as AddMode[]).map((mode) => (
            <Badge
              key={mode}
              render={<button type="button" />}
              variant={addMode === mode ? "default" : "outline"}
              onClick={() => setAddMode(mode)}
            >
              {mode === "install" ? t("skills.modeInstall") : t("skills.modeUpload")}
            </Badge>
          ))}
        </div>

        {addMode === "install" ? (
          <div className="space-y-1.5">
            <label className="text-xs font-medium text-muted-foreground">
              {t("skills.source")}
            </label>
            <Input
              type="text"
              value={source}
              onChange={(e) => setSource(e.target.value)}
              placeholder={t("skills.sourcePlaceholder")}
              autoComplete="off"
              nativeInput
            />
          </div>
        ) : (
          <div className="space-y-1.5">
            <label className="text-xs font-medium text-muted-foreground">{t("skills.file")}</label>
            <input
              type="file"
              accept=".zip"
              onChange={(e) => setUploadFile(e.target.files?.[0] ?? null)}
              className="block w-full text-xs text-muted-foreground file:mr-3 file:cursor-pointer file:rounded-md file:border file:border-border file:bg-muted file:px-3 file:py-1.5 file:text-xs file:font-medium file:text-foreground"
            />
          </div>
        )}

        <div className="flex items-center justify-end gap-2 pt-1">
          <Button size="sm" variant="ghost" onClick={() => setAddSheetOpen(false)}>
            {t("common.cancel")}
          </Button>
          <Button size="sm" loading={saving} onClick={installSkill}>
            {addMode === "install" ? t("skills.installSkill") : t("skills.uploadSkill")}
          </Button>
        </div>
      </div>
    </DetailPanel>
  );

  const detailPanel = detailSkill ? (
    <DetailPanel>
      <DetailPanelHeader
        title={<span className="font-mono">{detailSkill.name}</span>}
        subtitle={
          <div className="flex items-center gap-1.5">
            <Badge variant="outline" size="sm">
              {t(SCOPE_LABEL_KEY[detailSkill.scope as ScopedSkillScope])}
            </Badge>
            {detailSkill.agent_id && (
              <Badge variant="secondary" size="sm">
                {agentName(detailSkill.agent_id)}
              </Badge>
            )}
          </div>
        }
      />

      {detailSkill.description && (
        <p className="text-xs leading-relaxed text-muted-foreground">{detailSkill.description}</p>
      )}

      {detailLoading ? (
        <div className="flex justify-center py-8">
          <Spinner className="size-5" />
        </div>
      ) : (
        <div className="space-y-3 border-t border-border pt-4">
          <div className="flex flex-wrap items-center gap-2">
            <select
              value={detailFile}
              onChange={(e) => void openDetailFile(detailSkill.id, e.target.value)}
              className="min-w-0 max-w-full rounded-lg border border-border bg-background px-3 py-1.5 text-xs font-mono outline-none cursor-pointer sm:max-w-sm"
            >
              {(detailSkill.files ?? ["SKILL.md"]).map((f) => (
                <option key={f} value={f}>
                  {f}
                </option>
              ))}
            </select>
          </div>
          {detailFileLoading ? (
            <div className="flex justify-center py-8">
              <Spinner className="size-5" />
            </div>
          ) : (
            <SkillFilePreview
              path={detailFile}
              content={detailFileContent}
              encoding={detailFileEncoding}
              emptyText={t("skills.noContent")}
            />
          )}
        </div>
      )}
    </DetailPanel>
  ) : null;

  return (
    <>
      <SettingsGridPage
        title={t("skills.title")}
        action={
          <Button variant="ghost" size="xs" onClick={openAddSheet} className="cursor-pointer">
            <Plus className="size-3.5" />
            {t("skills.addSkill")}
          </Button>
        }
      >
        <SettingsSection
          icon={<Puzzle className="size-4" />}
          title={t("skills.tab.skills")}
          count={skills.length}
        >
          {loading && <p className="text-sm text-muted-foreground">{t("common.loading")}</p>}

          <div className="space-y-5">
            {skillGroups.map((group) => {
              const color = SCOPE_COLOR[group.scope];
              return (
                <div key={group.scope} className="space-y-2">
                  <div className="flex items-center gap-2 px-1">
                    <span className={`size-2 shrink-0 rounded-full ${color.dot}`} />
                    <span className="text-xs font-semibold text-foreground">
                      {t(SCOPE_LABEL_KEY[group.scope])}
                    </span>
                    <span className="text-xs text-muted-foreground">{group.items.length}</span>
                  </div>
                  <SettingsList>
                    {group.items.map((skill) => {
                      // Mirror the backend write rules instead of offering a
                      // toggle or a delete that comes back 403.
                      const readOnly = isSkillReadOnly(skill.scope, isAdmin);
                      return (
                        <SettingsRow
                          key={`${skill.scope}:${skill.agent_id ?? ""}:${skill.id}`}
                          icon={<Puzzle className="size-4" />}
                          title={<span className="font-mono">{skill.name}</span>}
                          chip={
                            skill.agent_id ? (
                              <Badge variant="outline" size="sm">
                                {agentName(skill.agent_id)}
                              </Badge>
                            ) : undefined
                          }
                          subtitle={
                            skill.description ||
                            t("skills.updatedCreated", {
                              updated: formatTime(skill.updated_at ?? ""),
                              created: formatTime(skill.created_at ?? ""),
                            })
                          }
                          status={
                            // Read-only scopes still show their state — the flag
                            // lives in the skill's frontmatter — but it cannot be
                            // changed from here.
                            <Switch
                              checked={!skill.disable_model_invocation}
                              disabled={readOnly}
                              onCheckedChange={() => void toggleModelInvocation(skill)}
                              title={
                                skill.disable_model_invocation
                                  ? t("skills.enable")
                                  : t("skills.disable")
                              }
                            />
                          }
                          primary={
                            readOnly ? (
                              <Tooltip>
                                <TooltipTrigger
                                  render={
                                    // Focusable on purpose: the lock is the only
                                    // explanation a read-only row carries.
                                    <span
                                      tabIndex={0}
                                      role="note"
                                      className="flex size-8 shrink-0 items-center justify-center text-muted-foreground"
                                      aria-label={t("skills.readOnly")}
                                    />
                                  }
                                >
                                  <Lock size={16} />
                                </TooltipTrigger>
                                <TooltipPopup side="top" className="max-w-56">
                                  {t(SCOPE_DESC_KEY[skill.scope as ScopedSkillScope])}
                                </TooltipPopup>
                              </Tooltip>
                            ) : undefined
                          }
                          onClick={() => void openDetail(skill)}
                          menu={
                            readOnly
                              ? undefined
                              : [
                                  {
                                    label: t("common.delete"),
                                    destructive: true,
                                    onClick: () => setPendingDelete(skill),
                                  },
                                ]
                          }
                        />
                      );
                    })}
                  </SettingsList>
                </div>
              );
            })}
          </div>

          {skillGroups.length === 0 && !loading && (
            <p className="py-4 text-center text-sm text-muted-foreground">{t("skills.noSkills")}</p>
          )}
        </SettingsSection>
      </SettingsGridPage>

      <SettingsDetailSheet open={!!detailSkill} onClose={() => setDetailSkill(null)}>
        {detailPanel}
      </SettingsDetailSheet>

      <SettingsDetailSheet open={addSheetOpen} onClose={() => setAddSheetOpen(false)}>
        {addPanel}
      </SettingsDetailSheet>

      <AlertDialog open={!!pendingDelete} onOpenChange={(open) => !open && setPendingDelete(null)}>
        <AlertDialogPopup>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("skills.deleteConfirmTitle")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("skills.deleteConfirmDesc", { name: pendingDelete?.name ?? "" })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogClose render={<Button variant="ghost" />}>
              {t("common.cancel")}
            </AlertDialogClose>
            <Button
              variant="destructive"
              onClick={() => {
                const skill = pendingDelete;
                setPendingDelete(null);
                if (skill) void deleteSkill(skill);
              }}
            >
              {t("common.delete")}
            </Button>
          </AlertDialogFooter>
        </AlertDialogPopup>
      </AlertDialog>
    </>
  );
}
