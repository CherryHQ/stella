import { useEffect, useRef, useState } from "react";
import { useNavigate, useParams } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { agentSkillsOptions } from "@/lib/queries/agents";
import { meQueryOptions } from "@/lib/queries/me";
import { cn } from "@/lib/utils";
import { useI18n } from "@/lib/i18n";
import { useAppShell } from "@/layouts/AppShell";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Collapsible, CollapsiblePanel, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Switch } from "@/components/ui/switch";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Spinner } from "@/components/ui/spinner";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { SkillFilePreview } from "@/features/sessions/SkillFilePreview";
import {
  Dialog,
  DialogPopup,
  DialogTitle,
  DialogHeader,
  DialogDescription,
  DialogPanel,
} from "@/components/ui/dialog";
import {
  installAgentSkill,
  searchSkills as sdkSearchSkills,
  uploadAgentSkill,
} from "@/lib/api-client/sdk.gen";
import {
  createAgentSkill,
  deleteAgentSkill,
  getAgentSkill,
  getAgentSkillFile,
  updateAgentSkill,
} from "@/lib/api-client";
import { ChevronRight, Plus, Code2, Cpu, Terminal, User, Bot, Upload, Search } from "lucide-react";
import type { Skill, SkillSearchResult } from "@/lib/types";
import { Route } from "@/routes/_app/agents.$agentId/skills/index";

interface SkillSectionProps {
  title: string;
  description?: string;
  count?: number;
  defaultOpen?: boolean;
  action?: React.ReactNode;
  children: React.ReactNode;
}

function SkillSection({
  title,
  description,
  count,
  defaultOpen = false,
  action,
  children,
}: SkillSectionProps) {
  return (
    <Collapsible defaultOpen={defaultOpen}>
      <div className="border-b border-border">
        <div className="flex items-center justify-between gap-3 py-3">
          <CollapsibleTrigger className="flex flex-1 items-center gap-2 py-1 text-left cursor-pointer group">
            <ChevronRight className="size-3.5 text-muted-foreground transition-transform duration-150 ease-out group-data-[panel-open]:rotate-90" />
            <div className="min-w-0 flex items-center gap-2">
              <span className="text-sm font-semibold">
                {title}
                {count != null && count > 0 && (
                  <span className="ml-1.5 text-xs font-normal text-muted-foreground">
                    ({count})
                  </span>
                )}
              </span>
              {description && (
                <span className="text-xs text-muted-foreground hidden group-data-[panel-open]:hidden sm:inline">
                  — {description}
                </span>
              )}
            </div>
          </CollapsibleTrigger>
          {action && <div className="shrink-0">{action}</div>}
        </div>
      </div>
      <CollapsiblePanel>
        <div className="py-4">{children}</div>
      </CollapsiblePanel>
    </Collapsible>
  );
}

export function SkillsListPage() {
  const { agentId } = useParams({ from: "/_app/agents/$agentId/skills/" });
  const navigate = useNavigate();
  const { t } = useI18n();
  const { setHeaderTitle, setHeaderActions } = useAppShell();
  const { data: skills = [], isLoading, refetch } = useQuery(agentSkillsOptions(agentId));

  const search = Route.useSearch();

  const [isNewDialogOpen, setIsNewDialogOpen] = useState(search.new === true);

  // Expanded skill key is `scope:name`
  const [expandedSkillId, setExpandedSkillId] = useState<string | null>(
    search.expand ? `${search.scope || "user"}:${search.expand}` : null,
  );

  // Sync state from query parameters if they change
  useEffect(() => {
    if (search.new) {
      setIsNewDialogOpen(true);
    }
  }, [search.new]);

  useEffect(() => {
    if (search.expand) {
      setExpandedSkillId(`${search.scope || "user"}:${search.expand}`);
    }
  }, [search.expand, search.scope]);

  useEffect(() => {
    setHeaderTitle(<span className="text-sm font-medium">{t("sessions.sidebar.skills")}</span>);
    setHeaderActions(null);
    return () => {
      setHeaderTitle(null);
      setHeaderActions(null);
    };
  }, [setHeaderActions, setHeaderTitle, t]);

  const systemSkills = skills.filter((s) => s.scope === "system");
  const agentSkills = skills.filter((s) => s.scope === "agent");
  const userSkills = skills.filter((s) => s.scope === "user");

  const handleToggleExpand = (key: string) => {
    setExpandedSkillId((current) => {
      const next = current === key ? null : key;
      if (next === null) {
        void navigate({
          to: "/agents/$agentId/skills",
          params: { agentId },
          search: {},
          replace: true,
        });
      } else {
        const [scope, skillId] = next.split(":");
        void navigate({
          to: "/agents/$agentId/skills",
          params: { agentId },
          search: { expand: skillId, scope },
          replace: true,
        });
      }
      return next;
    });
  };

  const handleCloseDialog = () => {
    setIsNewDialogOpen(false);
    void navigate({
      to: "/agents/$agentId/skills",
      params: { agentId },
      search: {},
      replace: true,
    });
  };

  const handleOpenDialog = () => {
    setIsNewDialogOpen(true);
    void navigate({
      to: "/agents/$agentId/skills",
      params: { agentId },
      search: { new: true },
      replace: true,
    });
  };

  const handleRefresh = () => {
    void refetch();
  };

  return (
    <div className="flex-1 overflow-y-auto">
      {isLoading ? (
        <div className="flex h-[200px] items-center justify-center">
          <div className="size-4 animate-spin rounded-full border-2 border-muted-foreground/30 border-t-muted-foreground" />
        </div>
      ) : (
        <div className="mx-auto max-w-3xl p-6 sm:p-8 lg:p-10 space-y-2">
          {/* User Skills Section */}
          <SkillSection
            title={t("sessions.skillsList.user")}
            count={userSkills.length}
            defaultOpen
            action={
              <Button
                variant="ghost"
                size="sm"
                onClick={handleOpenDialog}
                className="h-8 rounded-lg text-xs gap-1.5 px-2.5"
              >
                <Plus className="size-3.5" />
                {t("sessions.skill.newSkill")}
              </Button>
            }
          >
            {userSkills.length === 0 ? (
              <div className="rounded-xl border border-dashed border-border/80 p-6 text-center">
                <p className="text-xs text-muted-foreground mb-3 font-mono">
                  {t("sessions.skillsList.noSkills")}
                </p>
                <Button
                  variant="outline"
                  size="sm"
                  onClick={handleOpenDialog}
                  className="text-xs gap-1.5 rounded-lg"
                >
                  <Plus className="size-3.5" />
                  {t("sessions.skill.newSkill")}
                </Button>
              </div>
            ) : (
              <div className="divide-y divide-border/40 border-t border-b border-border/40">
                {userSkills.map((s) => {
                  const key = `user:${s.name}`;
                  return (
                    <SkillDetailRow
                      key={s.id}
                      agentId={agentId}
                      skill={s}
                      isExpanded={expandedSkillId === key}
                      onToggle={() => handleToggleExpand(key)}
                      onSaved={handleRefresh}
                      onDeleted={handleRefresh}
                    />
                  );
                })}
              </div>
            )}
          </SkillSection>

          {/* Agent Skills Section */}
          <SkillSection
            title={t("sessions.skillsList.agent")}
            count={agentSkills.length}
            defaultOpen
          >
            {agentSkills.length === 0 ? (
              <p className="text-xs text-muted-foreground italic pl-6 py-2">
                No agent-specific skills installed.
              </p>
            ) : (
              <div className="divide-y divide-border/40 border-t border-b border-border/40">
                {agentSkills.map((s) => {
                  const key = `agent:${s.name}`;
                  return (
                    <SkillDetailRow
                      key={s.id}
                      agentId={agentId}
                      skill={s}
                      isExpanded={expandedSkillId === key}
                      onToggle={() => handleToggleExpand(key)}
                      onSaved={handleRefresh}
                      onDeleted={handleRefresh}
                    />
                  );
                })}
              </div>
            )}
          </SkillSection>

          {/* System Skills Section */}
          <SkillSection
            title={t("sessions.skillsList.system")}
            count={systemSkills.length}
            defaultOpen={false}
          >
            {systemSkills.length === 0 ? (
              <p className="text-xs text-muted-foreground italic pl-6 py-2">No system skills.</p>
            ) : (
              <div className="divide-y divide-border/40 border-t border-b border-border/40">
                {systemSkills.map((s) => {
                  const key = `system:${s.name}`;
                  return (
                    <SkillDetailRow
                      key={s.id}
                      agentId={agentId}
                      skill={s}
                      isExpanded={expandedSkillId === key}
                      onToggle={() => handleToggleExpand(key)}
                      onSaved={handleRefresh}
                      onDeleted={handleRefresh}
                    />
                  );
                })}
              </div>
            )}
          </SkillSection>
        </div>
      )}

      {/* Dialog for installing / creating skill */}
      <NewSkillDialog
        agentId={agentId}
        isOpen={isNewDialogOpen}
        onClose={handleCloseDialog}
        onInstalled={handleRefresh}
      />
    </div>
  );
}

function SkillDetailRow({
  agentId,
  skill,
  isExpanded,
  onToggle,
  onSaved,
  onDeleted,
}: {
  agentId: string;
  skill: Skill;
  isExpanded: boolean;
  onToggle: () => void;
  onSaved: () => void;
  onDeleted: () => void;
}) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const [isEditing, setIsEditing] = useState(false);
  const [activeFile, setActiveFile] = useState("SKILL.md");
  const [fileLoading, setFileLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [deleting, setDeleting] = useState(false);

  const [form, setForm] = useState({
    description: "",
    status: "active" as "active" | "draft" | "deprecated",
    disable_model_invocation: false,
    content: "",
  });

  const [savedForm, setSavedForm] = useState({
    description: "",
    status: "active" as "active" | "draft" | "deprecated",
    disable_model_invocation: false,
    content: "",
  });

  const { data: detail, isLoading: detailLoading } = useQuery({
    queryKey: ["agent-skill-detail", agentId, skill.scope, skill.name],
    queryFn: async () => {
      const { data: sk } = await getAgentSkill({
        path: { id: agentId, skillId: skill.name },
        query: { scope: skill.scope as any },
        throwOnError: true,
      });

      const skillFiles = sk.files?.length ? sk.files : ["SKILL.md"];
      const initialFile = skillFiles.includes("SKILL.md") ? "SKILL.md" : skillFiles[0];

      const res = await getAgentSkillFile({
        path: { id: agentId, skillId: skill.name },
        query: { path: initialFile, scope: skill.scope as any },
        throwOnError: true,
      }).catch(() => null);

      const content = (res?.data as { content?: string })?.content ?? "";

      const initialForm = {
        description: sk.description ?? "",
        status: (sk.status as any) ?? "active",
        disable_model_invocation: sk.disable_model_invocation ?? false,
        content,
      };

      setForm(initialForm);
      setSavedForm(initialForm);
      setActiveFile(initialFile);

      return {
        skill: sk,
        files: skillFiles,
        content,
      };
    },
    enabled: isExpanded,
  });

  const isReadOnly = skill.scope === "system";

  const handleFileChange = async (path: string) => {
    setActiveFile(path);
    setFileLoading(true);
    try {
      const res = await getAgentSkillFile({
        path: { id: agentId, skillId: skill.name },
        query: { path, scope: skill.scope as any },
        throwOnError: true,
      });
      const content = (res.data as { content?: string })?.content ?? "";
      setForm((f) => ({ ...f, content }));
      setSavedForm((f) => ({ ...f, content }));
    } catch (e) {
      console.error(e);
    } finally {
      setFileLoading(false);
    }
  };

  const handleSave = async () => {
    setSaving(true);
    try {
      await updateAgentSkill({
        path: { id: agentId, skillId: skill.name },
        query: { scope: skill.scope as any },
        body: {
          description: form.description,
          status: form.status,
          disable_model_invocation: form.disable_model_invocation,
          files: { [activeFile]: form.content },
        },
        throwOnError: true,
      });
      setSavedForm(form);
      setIsEditing(false);
      onSaved();
      void queryClient.invalidateQueries({
        queryKey: ["agent-skill-detail", agentId, skill.scope, skill.name],
      });
    } catch (e) {
      console.error(e);
    } finally {
      setSaving(false);
    }
  };

  const handleDelete = async () => {
    if (!confirm(`Are you sure you want to delete the skill "${skill.name}"?`)) return;
    setDeleting(true);
    try {
      await deleteAgentSkill({
        path: { id: agentId, skillId: skill.name },
        query: { scope: skill.scope as any },
        throwOnError: true,
      });
      onDeleted();
    } catch (e) {
      console.error(e);
    } finally {
      setDeleting(false);
    }
  };

  const handleCancel = () => {
    setForm(savedForm);
    setIsEditing(false);
  };

  const isDirty = JSON.stringify(form) !== JSON.stringify(savedForm);

  return (
    <Collapsible open={isExpanded}>
      <div
        onClick={onToggle}
        className={cn(
          "group flex items-center justify-between gap-4 py-3 px-3 transition-all duration-150 cursor-pointer rounded-lg",
          isExpanded ? "bg-muted/30" : "hover:bg-muted/40",
        )}
      >
        <div className="flex items-start gap-3 min-w-0 flex-1">
          <div
            className={cn(
              "mt-0.5 shrink-0 rounded-md p-1.5 transition-colors",
              isExpanded
                ? "bg-background text-primary"
                : "bg-muted text-muted-foreground group-hover:bg-background group-hover:text-primary",
            )}
          >
            {skill.scope === "user" ? (
              <Code2 className="size-4" />
            ) : skill.scope === "agent" ? (
              <Cpu className="size-4" />
            ) : (
              <Terminal className="size-4" />
            )}
          </div>
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-2 flex-wrap">
              <span
                className={cn(
                  "text-[13px] font-medium leading-tight transition-colors font-mono",
                  isExpanded ? "text-primary" : "text-foreground group-hover:text-primary",
                )}
              >
                {skill.name}
              </span>

              {skill.status && skill.status !== "active" && (
                <Badge
                  variant={
                    skill.status === "deprecated"
                      ? "destructive"
                      : skill.status === "draft"
                        ? "warning"
                        : "outline"
                  }
                  size="sm"
                  className="text-[9px] uppercase px-1 py-0 h-4"
                >
                  {skill.status}
                </Badge>
              )}

              {skill.disable_model_invocation && (
                <Badge
                  variant="outline"
                  size="sm"
                  className="text-[9px] text-muted-foreground border-muted-foreground/30 px-1 py-0 h-4"
                >
                  {t("sessions.skill.modelInvocationLabel")} {t("common.disable")}
                </Badge>
              )}
            </div>
            {skill.description && !isExpanded && (
              <p className="text-[11px] text-muted-foreground mt-1 line-clamp-1">
                {skill.description}
              </p>
            )}
          </div>
        </div>
        <ChevronRight
          className={cn(
            "size-4 text-muted-foreground transition-all duration-150 shrink-0",
            isExpanded
              ? "rotate-90"
              : "opacity-0 group-hover:opacity-100 translate-x-[-4px] group-hover:translate-x-0",
          )}
        />
      </div>

      <CollapsiblePanel>
        <div className="px-3 pb-4 pt-1 ml-11 border-l border-border/60 space-y-4">
          {detailLoading ? (
            <div className="flex py-6 items-center gap-2 text-xs text-muted-foreground font-mono">
              <Spinner className="size-3.5" />
              Loading skill details...
            </div>
          ) : (
            <>
              {!isEditing && (
                <div className="space-y-4">
                  {form.description && (
                    <p className="text-xs text-muted-foreground font-mono leading-relaxed bg-muted/20 p-2.5 rounded-lg border border-border/40">
                      {form.description}
                    </p>
                  )}

                  {detail && detail.files.length > 1 && (
                    <div className="flex items-center gap-1.5 overflow-x-auto py-1">
                      {detail.files.map((file) => (
                        <button
                          key={file}
                          onClick={() => void handleFileChange(file)}
                          className={cn(
                            "px-2.5 py-1 text-xs font-mono rounded-md border transition-colors",
                            activeFile === file
                              ? "bg-muted text-foreground border-border"
                              : "text-muted-foreground hover:text-foreground border-transparent",
                          )}
                        >
                          {file}
                        </button>
                      ))}
                    </div>
                  )}

                  <div className="rounded-xl border border-border/60 bg-muted/5 overflow-hidden">
                    <div className="bg-muted/40 border-b border-border/50 px-3 py-1.5 flex justify-between items-center">
                      <span className="text-[10px] font-mono text-muted-foreground">
                        {activeFile}
                      </span>
                    </div>
                    <div className="p-3">
                      {fileLoading ? (
                        <div className="flex py-4 items-center justify-center text-xs text-muted-foreground gap-2">
                          <Spinner className="size-3.5" />
                          Loading file...
                        </div>
                      ) : (
                        <SkillFilePreview
                          path={activeFile}
                          content={form.content}
                          emptyText={t("sessions.skill.noContent")}
                          className="bg-transparent p-0 text-xs"
                        />
                      )}
                    </div>
                  </div>

                  <div className="flex items-center justify-between pt-1">
                    <div className="flex items-center gap-2 text-xs text-muted-foreground font-mono">
                      <span>{t("sessions.skill.modelInvocationLabel")}</span>
                      <span
                        className={cn(
                          "font-semibold",
                          form.disable_model_invocation
                            ? "text-destructive"
                            : "text-success-foreground",
                        )}
                      >
                        {form.disable_model_invocation ? t("common.disable") : t("common.enable")}
                      </span>
                    </div>

                    {!isReadOnly && (
                      <div className="flex items-center gap-2">
                        <Button
                          variant="outline"
                          size="sm"
                          onClick={() => setIsEditing(true)}
                          className="h-8 rounded-lg text-xs"
                        >
                          {t("common.edit")}
                        </Button>
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={() => void handleDelete()}
                          disabled={deleting}
                          className="h-8 rounded-lg text-xs text-destructive hover:text-destructive hover:bg-destructive/10"
                        >
                          {deleting ? t("sessions.skill.deleting") : t("common.delete")}
                        </Button>
                      </div>
                    )}
                  </div>
                </div>
              )}

              {isEditing && !isReadOnly && (
                <div className="space-y-4 rounded-xl border border-border bg-muted/10 p-4">
                  <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
                    <div>
                      <label className="block text-[11px] font-mono text-muted-foreground mb-1.5">
                        {t("sessions.skill.fieldStatus")}
                      </label>
                      <div className="inline-flex rounded-lg border border-border bg-background p-0.5">
                        {(["active", "draft", "deprecated"] as const).map((s) => (
                          <button
                            key={s}
                            type="button"
                            onClick={() => setForm((f) => ({ ...f, status: s }))}
                            className={cn(
                              "px-2.5 py-1 text-xs font-medium rounded-md capitalize transition-colors",
                              form.status === s
                                ? "bg-muted text-foreground"
                                : "text-muted-foreground hover:text-foreground",
                            )}
                          >
                            {s}
                          </button>
                        ))}
                      </div>
                    </div>

                    <div className="sm:col-span-2">
                      <label className="block text-[11px] font-mono text-muted-foreground mb-1">
                        {t("sessions.skill.fieldDescription")}
                      </label>
                      <Input
                        nativeInput
                        value={form.description}
                        onChange={(e) =>
                          setForm((f) => ({
                            ...f,
                            description: (e.target as HTMLInputElement).value,
                          }))
                        }
                        placeholder={t("sessions.skill.descPlaceholder")}
                        className="text-xs"
                      />
                    </div>

                    <div className="sm:col-span-2">
                      <label className="block text-[11px] font-mono text-muted-foreground mb-1">
                        {t("sessions.skill.fieldContent")} ({activeFile})
                      </label>
                      <Textarea
                        value={form.content}
                        onChange={(e) =>
                          setForm((f) => ({
                            ...f,
                            content: (e.target as HTMLTextAreaElement).value,
                          }))
                        }
                        rows={12}
                        placeholder={"# My Skill\n\nInstructions for the agent…"}
                        className="text-xs font-mono bg-background"
                      />
                    </div>
                  </div>

                  <div className="flex items-center justify-between py-2.5 border-t border-b border-border/40">
                    <div className="space-y-0.5">
                      <span className="text-xs font-medium block">
                        {t("sessions.skill.modelInvocation")}
                      </span>
                      <span className="text-[10px] text-muted-foreground block">
                        Allow LLM to automatically run this skill during conversations.
                      </span>
                    </div>
                    <Switch
                      checked={!form.disable_model_invocation}
                      onCheckedChange={(checked) =>
                        setForm((f) => ({ ...f, disable_model_invocation: !checked }))
                      }
                    />
                  </div>

                  <div className="flex items-center gap-2 justify-end pt-1">
                    <Button
                      onClick={() => void handleSave()}
                      disabled={saving || !isDirty}
                      size="sm"
                      className="h-8 text-xs rounded-lg"
                    >
                      {saving ? t("sessions.skill.saving") : t("common.save")}
                    </Button>
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={handleCancel}
                      disabled={saving}
                      className="h-8 text-xs rounded-lg"
                    >
                      {t("common.cancel")}
                    </Button>
                  </div>
                </div>
              )}
            </>
          )}
        </div>
      </CollapsiblePanel>
    </Collapsible>
  );
}

function NewSkillDialog({
  agentId,
  isOpen,
  onClose,
  onInstalled,
}: {
  agentId: string;
  isOpen: boolean;
  onClose: () => void;
  onInstalled: () => void;
}) {
  const { t } = useI18n();
  const { data: me } = useQuery(meQueryOptions);
  const canInstallAgentSkill = me?.is_admin ?? false;

  const [activeTab, setActiveTab] = useState<"catalog" | "upload" | "custom">("catalog");
  const [scope, setScope] = useState<"user" | "agent">("user");

  // Catalog state
  const [searchQuery, setSearchQuery] = useState("");
  const [searchResults, setSearchResults] = useState<SkillSearchResult[]>([]);
  const [searching, setSearching] = useState(false);
  const searchTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const [installing, setInstalling] = useState(false);
  const [installTarget, setInstallTarget] = useState("");
  const [installError, setInstallError] = useState("");

  // Upload state
  const [uploadFile, setUploadFile] = useState<File | null>(null);
  const [uploading, setUploading] = useState(false);
  const [uploadError, setUploadError] = useState("");

  // Custom state
  const [customForm, setCustomForm] = useState({
    name: "",
    description: "",
    status: "active" as "active" | "draft" | "deprecated",
    disable_model_invocation: false,
    content: "# My Skill\n\nInstructions for the agent…\n",
  });
  const [customCreating, setCustomCreating] = useState(false);
  const [customError, setCustomError] = useState("");

  const handleSearchChange = (q: string) => {
    setSearchQuery(q);
    if (searchTimerRef.current) clearTimeout(searchTimerRef.current);
    searchTimerRef.current = setTimeout(async () => {
      if (!q.trim()) {
        setSearchResults([]);
        return;
      }
      setSearching(true);
      try {
        const { data } = await sdkSearchSkills({ query: { q, limit: 20 }, throwOnError: true });
        setSearchResults((data?.skills as SkillSearchResult[]) ?? []);
        setInstallError("");
      } catch (e) {
        setInstallError((e as Error).message);
        setSearchResults([]);
      } finally {
        setSearching(false);
      }
    }, 300);
  };

  const handleInstall = async (source: string) => {
    setInstalling(true);
    setInstallTarget(source);
    setInstallError("");
    try {
      await installAgentSkill({
        path: { id: agentId },
        body: { source, scope },
        throwOnError: true,
      });
      onInstalled();
      onClose();
    } catch (e) {
      setInstallError((e as Error).message);
    } finally {
      setInstalling(false);
      setInstallTarget("");
    }
  };

  const handleUpload = async () => {
    if (!uploadFile) return;
    setUploading(true);
    setUploadError("");
    try {
      await uploadAgentSkill({
        path: { id: agentId },
        body: { file: uploadFile, scope },
        throwOnError: true,
      });
      onInstalled();
      onClose();
    } catch (e) {
      setUploadError((e as Error).message);
    } finally {
      setUploading(false);
    }
  };

  const handleCreateCustom = async () => {
    if (!customForm.name.trim()) {
      setCustomError("Name is required");
      return;
    }
    setCustomCreating(true);
    setCustomError("");
    try {
      await createAgentSkill({
        path: { id: agentId },
        body: {
          name: customForm.name.trim(),
          scope: "user",
          description: customForm.description,
          status: customForm.status,
          disable_model_invocation: customForm.disable_model_invocation,
          files: { "SKILL.md": customForm.content },
        },
        throwOnError: true,
      });
      onInstalled();
      onClose();
    } catch (e) {
      setCustomError((e as Error).message);
    } finally {
      setCustomCreating(false);
    }
  };

  return (
    <Dialog open={isOpen} onOpenChange={(open) => !open && onClose()}>
      <DialogPopup className="max-w-2xl w-full" showCloseButton>
        <DialogHeader>
          <DialogTitle className="text-lg font-semibold flex items-center gap-2">
            {t("sessions.skill.installSkill")}
          </DialogTitle>
          <DialogDescription className="text-xs text-muted-foreground font-mono">
            {t("sessions.skill.catalogDesc")}
          </DialogDescription>
        </DialogHeader>

        <DialogPanel className="space-y-4">
          {activeTab !== "custom" && (
            <div className="flex justify-end mb-1">
              <div className="inline-flex rounded-lg border border-border bg-background p-0.5 shadow-xs/5">
                <button
                  type="button"
                  onClick={() => setScope("user")}
                  className={cn(
                    "inline-flex items-center gap-1.5 px-3 py-1 text-xs font-medium rounded-md transition-colors",
                    scope === "user"
                      ? "bg-muted text-foreground"
                      : "text-muted-foreground hover:text-foreground",
                  )}
                >
                  <User className="size-3.5" />
                  {t("sessions.skill.myProfile")}
                </button>
                <button
                  type="button"
                  onClick={() => setScope("agent")}
                  disabled={!canInstallAgentSkill}
                  className={cn(
                    "inline-flex items-center gap-1.5 px-3 py-1 text-xs font-medium rounded-md transition-colors disabled:opacity-50",
                    scope === "agent"
                      ? "bg-muted text-foreground"
                      : "text-muted-foreground hover:text-foreground",
                  )}
                >
                  <Bot className="size-3.5" />
                  {t("sessions.skill.thisAgent")}
                </button>
              </div>
            </div>
          )}

          <Tabs value={activeTab} onValueChange={(t: any) => setActiveTab(t)} className="gap-0">
            <TabsList className="grid w-full grid-cols-3 mb-4">
              <TabsTrigger value="catalog" className="text-xs">
                <Search className="size-3.5 mr-1" />
                {t("sessions.skill.catalog")}
              </TabsTrigger>
              <TabsTrigger value="upload" className="text-xs">
                <Upload className="size-3.5 mr-1" />
                {t("sessions.skill.uploadZipTab")}
              </TabsTrigger>
              <TabsTrigger value="custom" className="text-xs">
                <Plus className="size-3.5 mr-1" />
                Custom Skill
              </TabsTrigger>
            </TabsList>

            <TabsContent value="catalog" className="space-y-4 outline-none">
              <div className="space-y-2">
                <Input
                  nativeInput
                  value={searchQuery}
                  onChange={(e) => handleSearchChange((e.target as HTMLInputElement).value)}
                  type="search"
                  placeholder={t("sessions.skill.searchSkills")}
                  className="text-sm"
                  autoFocus
                />
              </div>

              {searching && (
                <div className="flex justify-center py-12">
                  <Spinner className="size-5" />
                </div>
              )}

              {!searching && searchResults.length > 0 && (
                <div className="divide-y divide-border/40 rounded-xl border border-border bg-background/50 max-h-[300px] overflow-y-auto">
                  {searchResults.map((s) => {
                    const source = `${s.source}@${s.skillId}`;
                    return (
                      <div key={s.id} className="p-3.5 hover:bg-muted/10 transition-colors">
                        <div className="flex items-start justify-between gap-4">
                          <div className="min-w-0 flex-1">
                            <div className="flex items-center gap-2">
                              <p className="truncate font-mono text-xs font-semibold text-foreground">
                                {s.name || s.skillId}
                              </p>
                              <span className="shrink-0 text-[10px] bg-muted/60 px-1 py-0.5 rounded text-muted-foreground font-mono">
                                {s.installs} installs
                              </span>
                            </div>
                            <p className="mt-0.5 truncate font-mono text-[10px] text-muted-foreground">
                              {source}
                            </p>
                            {s.description && (
                              <p className="mt-2 text-xs leading-relaxed text-muted-foreground line-clamp-2">
                                {s.description}
                              </p>
                            )}
                          </div>
                          <Button
                            onClick={() => void handleInstall(source)}
                            disabled={installing}
                            loading={installing && installTarget === source}
                            size="sm"
                            className="h-8 text-xs rounded-lg shrink-0"
                          >
                            Install
                          </Button>
                        </div>
                      </div>
                    );
                  })}
                </div>
              )}

              {!searching && searchResults.length === 0 && searchQuery && (
                <div className="rounded-xl border border-dashed border-border/80 px-4 py-10 text-center text-xs text-muted-foreground">
                  {t("sessions.skill.noSkillsFound")}
                </div>
              )}

              {!searching && !searchQuery && (
                <div className="rounded-xl border border-dashed border-border/80 px-4 py-12 text-center space-y-2">
                  <Search className="mx-auto size-5 text-muted-foreground" />
                  <p className="text-xs font-semibold">{t("sessions.skill.searchCatalog")}</p>
                  <p className="text-[11px] text-muted-foreground">
                    {t("sessions.skill.resultsAppear")}
                  </p>
                </div>
              )}

              {installError && <p className="text-xs text-destructive font-mono">{installError}</p>}
            </TabsContent>

            <TabsContent value="upload" className="space-y-4 outline-none">
              <label className="block cursor-pointer rounded-xl border-2 border-dashed border-border/80 px-4 py-10 text-center transition-colors hover:border-primary/50 bg-muted/5 hover:bg-muted/10">
                <input
                  onChange={(e) => {
                    setUploadFile(e.target.files?.[0] ?? null);
                    setUploadError("");
                  }}
                  type="file"
                  accept=".zip,application/zip"
                  className="hidden"
                />
                <Upload className="mx-auto mb-2 size-6 text-muted-foreground" />
                <div className="text-xs font-semibold">
                  {uploadFile ? uploadFile.name : t("sessions.skill.chooseZip")}
                </div>
                <p className="mt-1 text-[10px] text-muted-foreground">
                  {t("sessions.skill.zipReq")}
                </p>
                <Button variant="outline" size="sm" className="mt-3 h-7 text-xs rounded-lg">
                  {t("sessions.skill.browseFiles")}
                </Button>
              </label>

              <div className="grid grid-cols-3 gap-2 text-center text-[10px] font-mono text-muted-foreground">
                <div className="rounded-lg border border-border/60 bg-muted/10 px-2 py-1.5">
                  .zip file
                </div>
                <div className="rounded-lg border border-border/60 bg-muted/10 px-2 py-1.5">
                  One folder
                </div>
                <div className="rounded-lg border border-border/60 bg-muted/10 px-2 py-1.5">
                  SKILL.md
                </div>
              </div>

              {uploadError && <p className="text-xs text-destructive font-mono">{uploadError}</p>}

              <Button
                onClick={() => void handleUpload()}
                disabled={uploading || !uploadFile}
                loading={uploading}
                className="w-full h-9 text-xs rounded-lg"
              >
                {t("sessions.skill.uploadSkillBtn")}
              </Button>
            </TabsContent>

            <TabsContent value="custom" className="space-y-4 outline-none">
              <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
                <div>
                  <label className="block text-[11px] font-mono text-muted-foreground mb-1">
                    {t("sessions.skill.fieldName")}
                  </label>
                  <Input
                    nativeInput
                    value={customForm.name}
                    onChange={(e) =>
                      setCustomForm((f) => ({ ...f, name: (e.target as HTMLInputElement).value }))
                    }
                    placeholder={t("sessions.skill.namePlaceholder")}
                    className="text-xs font-mono"
                  />
                </div>

                <div>
                  <label className="block text-[11px] font-mono text-muted-foreground mb-1.5">
                    {t("sessions.skill.fieldStatus")}
                  </label>
                  <div className="inline-flex rounded-lg border border-border bg-background p-0.5">
                    {(["active", "draft", "deprecated"] as const).map((s) => (
                      <button
                        key={s}
                        type="button"
                        onClick={() => setCustomForm((f) => ({ ...f, status: s }))}
                        className={cn(
                          "px-2.5 py-1 text-xs font-medium rounded-md capitalize transition-colors",
                          customForm.status === s
                            ? "bg-muted text-foreground"
                            : "text-muted-foreground hover:text-foreground",
                        )}
                      >
                        {s}
                      </button>
                    ))}
                  </div>
                </div>

                <div className="sm:col-span-2">
                  <label className="block text-[11px] font-mono text-muted-foreground mb-1">
                    {t("sessions.skill.fieldDescription")}
                  </label>
                  <Input
                    nativeInput
                    value={customForm.description}
                    onChange={(e) =>
                      setCustomForm((f) => ({
                        ...f,
                        description: (e.target as HTMLInputElement).value,
                      }))
                    }
                    placeholder={t("sessions.skill.descPlaceholder")}
                    className="text-xs"
                  />
                </div>

                <div className="sm:col-span-2">
                  <label className="block text-[11px] font-mono text-muted-foreground mb-1">
                    {t("sessions.skill.fieldContent")} (SKILL.md)
                  </label>
                  <Textarea
                    value={customForm.content}
                    onChange={(e) =>
                      setCustomForm((f) => ({
                        ...f,
                        content: (e.target as HTMLTextAreaElement).value,
                      }))
                    }
                    rows={8}
                    className="text-xs font-mono bg-background"
                  />
                </div>
              </div>

              <div className="flex items-center justify-between py-2 border-t border-b border-border/40">
                <div className="space-y-0.5">
                  <span className="text-xs font-medium block">
                    {t("sessions.skill.modelInvocation")}
                  </span>
                  <span className="text-[10px] text-muted-foreground block">
                    Allow LLM to automatically run this skill during conversations.
                  </span>
                </div>
                <Switch
                  checked={!customForm.disable_model_invocation}
                  onCheckedChange={(checked) =>
                    setCustomForm((f) => ({ ...f, disable_model_invocation: !checked }))
                  }
                />
              </div>

              {customError && <p className="text-xs text-destructive font-mono">{customError}</p>}

              <Button
                onClick={() => void handleCreateCustom()}
                disabled={customCreating || !customForm.name}
                loading={customCreating}
                className="w-full h-9 text-xs rounded-lg"
              >
                Create Skill
              </Button>
            </TabsContent>
          </Tabs>
        </DialogPanel>
      </DialogPopup>
    </Dialog>
  );
}
