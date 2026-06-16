import { useEffect, useMemo, useState } from "react";
import { useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Blocks, Check, Copy, FileText, GitBranch, Lock, Plus, Search, X } from "lucide-react";
import { useToast, ToastContainer } from "@/hooks/use-toast";
import { useIsMobile } from "@/hooks/use-mobile";
import { useAppShell } from "@/layouts/AppShell";
import { agentSkillsOptions } from "@/lib/queries/agents";
import { meQueryOptions } from "@/lib/queries/me";
import { useI18n } from "@/lib/i18n";
import { formatTime } from "@/lib/time";
import { cn } from "@/lib/utils";
import type { Skill, SkillSearchResult } from "@/lib/types";
import {
  deleteAgentSkill,
  getAgentSkill,
  getAgentSkillFile,
  installAgentSkill,
  searchSkills,
  updateAgentSkill,
  uploadAgentSkill,
} from "@/lib/api-client/sdk.gen";
import { apiErrorMessage } from "@/lib/api-error";
import { SkillFilePreview } from "@/features/sessions/SkillFilePreview";
import { SkillsDiscover } from "@/features/sessions/pages/SkillsDiscover";
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
import { Label } from "@/components/ui/label";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import {
  Dialog,
  DialogDescription,
  DialogHeader,
  DialogPanel,
  DialogPopup,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group";
import { Sheet, SheetPanel, SheetPopup } from "@/components/ui/sheet";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";

type Scope = "project" | "user" | "agent" | "system";
type Tab = "installed" | "discover";
const SCOPES: Scope[] = ["project", "user", "agent", "system"];
const WRITABLE = new Set<Scope>(["user", "agent"]);

function route(projectId?: string) {
  return projectId ? "/agents/$agentId/projects/$projectId/skills" : "/agents/$agentId/skills";
}

function statusLabelKey(status?: string) {
  if (status === "draft") return "sessions.skillsList.statusDraft" as const;
  if (status === "deprecated") return "sessions.skillsList.statusDeprecated" as const;
  return "sessions.skillsList.statusActive" as const;
}

function SkillGlyph({ className }: { className?: string }) {
  return (
    <div
      className={cn(
        "flex size-9 shrink-0 items-center justify-center rounded-lg border bg-card text-muted-foreground",
        className,
      )}
    >
      <Blocks className="size-5" />
    </div>
  );
}

export function SkillsListPage() {
  const { agentId, projectId } = useParams({ strict: false }) as {
    agentId: string;
    projectId?: string;
  };
  const search = useSearch({ strict: false }) as {
    tab?: Tab;
    expand?: string;
    scope?: Scope;
    new?: boolean;
  };
  const navigate = useNavigate();
  const { t } = useI18n();
  const isMobile = useIsMobile();
  const { setHeaderActions } = useAppShell();
  const { data: skills = [], isLoading } = useQuery(agentSkillsOptions(agentId));
  const [query, setQuery] = useState("");
  const [scopeFilter, setScopeFilter] = useState<Scope | "all">("all");
  const [installOpen, setInstallOpen] = useState(Boolean(search.new));
  const activeTab = search.tab === "discover" ? "discover" : "installed";
  const params = projectId ? { agentId, projectId } : { agentId };
  const selected =
    search.expand && search.scope
      ? skills.find((s) => s.name === search.expand && s.scope === search.scope)
      : undefined;

  function setTab(tab: string) {
    void navigate({
      to: route(projectId),
      params,
      search: tab === "discover" ? { tab: "discover" } : {},
      replace: true,
    });
  }
  function selectSkill(skill?: Skill) {
    void navigate({
      to: route(projectId),
      params,
      search: skill
        ? {
            tab: activeTab === "discover" ? "discover" : undefined,
            expand: skill.name,
            scope: skill.scope,
          }
        : activeTab === "discover"
          ? { tab: "discover" }
          : {},
      replace: true,
    });
  }

  useEffect(() => {
    setHeaderActions(
      <div className="flex items-center gap-1">
        <Button
          size="sm"
          variant={activeTab === "installed" ? "secondary" : "outline"}
          aria-pressed={activeTab === "installed"}
          onClick={() => setTab("installed")}
        >
          <Blocks />
          <span className="max-sm:hidden">{t("sessions.skillsList.installedTab")}</span>
          <span className="max-sm:hidden">{skills.length}</span>
        </Button>
        <Button
          size="sm"
          variant={activeTab === "discover" ? "secondary" : "outline"}
          aria-pressed={activeTab === "discover"}
          onClick={() => setTab("discover")}
        >
          <Search />
          <span className="max-sm:hidden">{t("sessions.skillsList.discoverTab")}</span>
        </Button>
      </div>,
    );
    return () => setHeaderActions(null);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeTab, skills.length, t]);

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
        event.preventDefault();
        setInstallOpen(true);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    return skills.filter(
      (s) =>
        (scopeFilter === "all" || s.scope === scopeFilter) &&
        (!q || s.name.toLowerCase().includes(q) || (s.description ?? "").toLowerCase().includes(q)),
    );
  }, [skills, query, scopeFilter]);
  const counts = Object.fromEntries(
    SCOPES.map((scope) => [scope, skills.filter((s) => s.scope === scope).length]),
  ) as Record<Scope, number>;
  const installedNames = new Set(skills.map((s) => s.name));
  const installedSources = new Set(
    skills.map((s) => s.source).filter((src): src is string => !!src),
  );
  const sections = SCOPES.map((scope) => ({
    scope,
    items: filtered.filter((s) => s.scope === scope),
  })).filter(
    ({ scope, items }) =>
      (scopeFilter === "all" || scopeFilter === scope) &&
      (items.length > 0 || (scope === "user" && !query.trim())),
  );

  return (
    <div className="flex h-full min-h-0 flex-col overflow-hidden">
      {activeTab === "discover" ? (
        <SkillsDiscover
          agentId={agentId}
          installedNames={installedNames}
          installedSources={installedSources}
        />
      ) : (
        <div className="flex h-full min-h-0">
          <div
            className={cn(
              "flex min-h-0 shrink-0 flex-col border-r max-md:w-full max-md:border-r-0",
              "w-full md:w-[360px]",
              selected && isMobile ? "max-md:hidden" : "",
            )}
          >
            <div className="flex flex-col gap-3 border-b p-3">
              <InputGroup>
                <InputGroupAddon>
                  <Search />
                </InputGroupAddon>
                <InputGroupInput
                  nativeInput
                  value={query}
                  onChange={(e) => setQuery((e.target as HTMLInputElement).value)}
                  placeholder={t("sessions.skillsList.searchPlaceholder")}
                />
              </InputGroup>
              <div className="flex flex-wrap gap-1">
                {(["all", ...SCOPES] as const).map((scope) => (
                  <Button
                    key={scope}
                    size="sm"
                    variant={scopeFilter === scope ? "secondary" : "ghost"}
                    onClick={() => setScopeFilter(scope)}
                  >
                    {t(`sessions.skillsList.${scope}`)}{" "}
                    <span className="text-muted-foreground">
                      {scope === "all" ? skills.length : counts[scope]}
                    </span>
                  </Button>
                ))}
              </div>
            </div>
            <div className="min-h-0 flex-1 overflow-y-auto p-2">
              {isLoading ? (
                <div className="flex h-48 items-center justify-center">
                  <Spinner />
                </div>
              ) : sections.length === 0 ? (
                <p className="px-3 py-8 text-center text-sm text-muted-foreground">
                  {t("sessions.skillsList.noSkills")}
                </p>
              ) : (
                sections.map(({ scope, items }) => (
                  <div key={scope} className="mb-3 last:mb-0">
                    <div className="flex items-center gap-2 px-2 py-1.5">
                      <span className="text-xs font-medium text-muted-foreground">
                        {t(`sessions.skillsList.${scope}`)} · {items.length}
                      </span>
                      {!WRITABLE.has(scope) && (
                        <span className="inline-flex items-center gap-1 text-xs text-muted-foreground/70">
                          <Lock className="size-4" />
                          {t("sessions.skillsList.readonly")}
                        </span>
                      )}
                      {scope === "user" && (
                        <Button
                          size="xs"
                          variant="ghost"
                          className="ml-auto"
                          onClick={() => setInstallOpen(true)}
                        >
                          <Plus size={16} />
                          {t("sessions.skill.installSkill")}
                        </Button>
                      )}
                    </div>
                    <div className="space-y-0.5">
                      {items.map((skill) => (
                        <SkillRow
                          key={skill.id}
                          skill={skill}
                          selected={selected?.id === skill.id}
                          onSelect={() => selectSkill(skill)}
                        />
                      ))}
                      {items.length === 0 && (
                        <p className="px-3 py-2 text-xs text-muted-foreground">
                          {t("sessions.skillsList.noSkills")}
                        </p>
                      )}
                    </div>
                  </div>
                ))
              )}
            </div>
          </div>
          {/* Desktop detail pane */}
          <div className="hidden min-h-0 min-w-0 flex-1 md:flex">
            {selected ? (
              <SkillInspector agentId={agentId} skill={selected} onClose={() => selectSkill()} />
            ) : (
              <div className="flex flex-1 flex-col items-center justify-center gap-3 text-muted-foreground">
                <SkillGlyph className="size-12 rounded-xl" />
                <p className="text-sm">{t("sessions.skillsList.selectHint")}</p>
              </div>
            )}
          </div>
        </div>
      )}
      {/* Mobile detail sheet */}
      {selected && isMobile && (
        <Sheet open onOpenChange={(open) => !open && selectSkill()}>
          <SheetPopup side="right">
            <SheetPanel className="p-0">
              <SkillInspector agentId={agentId} skill={selected} onClose={() => selectSkill()} />
            </SheetPanel>
          </SheetPopup>
        </Sheet>
      )}
      <InstallDialog agentId={agentId} open={installOpen} onOpenChange={setInstallOpen} />
      <ToastContainer messages={useToast().toasts} />
    </div>
  );
}

function SkillRow({
  skill,
  selected,
  onSelect,
}: {
  skill: Skill;
  selected: boolean;
  onSelect: () => void;
}) {
  const { t } = useI18n();
  return (
    <button
      type="button"
      onClick={onSelect}
      className={cn(
        "flex w-full items-start gap-3 rounded-lg px-2.5 py-2.5 text-left transition-colors",
        selected ? "bg-accent" : "hover:bg-muted",
      )}
    >
      <SkillGlyph />
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className="truncate font-mono text-sm font-medium">{skill.name}</span>
          {skill.status !== "active" && (
            <Badge variant="secondary" size="sm">
              {t(statusLabelKey(skill.status))}
            </Badge>
          )}
          {skill.disable_model_invocation && (
            <Badge variant="outline" size="sm">
              {t("sessions.skillsList.manual")}
            </Badge>
          )}
        </div>
        <p className="mt-0.5 truncate text-xs text-muted-foreground">{skill.description}</p>
      </div>
      <span className="shrink-0 text-xs text-muted-foreground">{formatTime(skill.updated_at)}</span>
    </button>
  );
}

function SkillInspector({
  agentId,
  skill,
  onClose,
}: {
  agentId: string;
  skill: Skill;
  onClose?: () => void;
}) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { showToast } = useToast();
  const [tab, setTab] = useState("overview");
  const [description, setDescription] = useState(skill.description ?? "");
  const [status, setStatus] = useState(skill.status ?? "active");
  const [modelEnabled, setModelEnabled] = useState(!skill.disable_model_invocation);
  const [viewer, setViewer] = useState<string | null>(null);
  const [confirmOpen, setConfirmOpen] = useState(false);
  const readOnly = !WRITABLE.has(skill.scope as Scope);
  const detail = useQuery({
    queryKey: ["agent-skill", agentId, skill.scope, skill.name],
    queryFn: async () =>
      (
        await getAgentSkill({
          path: { id: agentId, skillId: skill.name },
          query: { scope: skill.scope as Scope },
          throwOnError: true,
        })
      ).data as Skill,
  });
  const files = detail.data?.files ?? skill.files ?? [];
  async function save() {
    try {
      await updateAgentSkill({
        path: { id: agentId, skillId: skill.name },
        query: { scope: skill.scope as Scope },
        body: { description, status, disable_model_invocation: !modelEnabled },
        throwOnError: true,
      });
      showToast(t("sessions.skillsList.saved"), "success");
      void queryClient.invalidateQueries({ queryKey: ["agent-skills", agentId] });
    } catch (error) {
      showToast(apiErrorMessage(error, t("common.error")), "error");
    }
  }
  async function remove() {
    try {
      await deleteAgentSkill({
        path: { id: agentId, skillId: skill.name },
        query: { scope: skill.scope as Scope },
        throwOnError: true,
      });
      showToast(t("common.delete"), "success");
      await queryClient.invalidateQueries({ queryKey: ["agent-skills", agentId] });
      onClose?.();
    } catch (error) {
      showToast(apiErrorMessage(error, t("common.error")), "error");
    } finally {
      setConfirmOpen(false);
    }
  }
  return (
    <div className="flex h-full min-h-0 flex-1 flex-col">
      <div className="border-b p-5">
        <div className="flex items-start gap-3">
          <SkillGlyph className="size-11 rounded-lg" />
          <div className="min-w-0 flex-1">
            <h2 className="truncate font-mono text-base font-semibold">{skill.name}</h2>
            <div className="mt-2 flex flex-wrap items-center gap-2">
              <Badge variant="secondary" size="sm">
                {t(`sessions.skillsList.${skill.scope}`)}
              </Badge>
              {skill.status !== "active" ? (
                <Badge variant="outline" size="sm">
                  {t(statusLabelKey(skill.status))}
                </Badge>
              ) : (
                <Badge variant="success" size="sm">
                  <Check />
                  {t("sessions.skillsList.statusActive")}
                </Badge>
              )}
              <Badge variant="outline" size="sm">
                {skill.disable_model_invocation
                  ? t("sessions.skillsList.manual")
                  : t("sessions.skillsList.auto")}
              </Badge>
            </div>
          </div>
          {onClose && (
            <Button size="icon-sm" variant="ghost" onClick={onClose}>
              <X size={16} />
            </Button>
          )}
        </div>
        <div className="mt-3 flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground">
          <span className="inline-flex items-center gap-1">
            <FileText className="size-4" />
            {t("sessions.skillsList.fileCount")} {files.length}
          </span>
          <span className="inline-flex items-center gap-1">
            <Lock className="size-4" />
            {formatTime(skill.updated_at)}
          </span>
          {skill.source && (
            <span className="inline-flex items-center gap-1 font-mono">
              <GitBranch className="size-4" />
              {skill.source}
            </span>
          )}
        </div>
      </div>
      <Tabs value={tab} onValueChange={setTab} className="flex min-h-0 flex-1 flex-col">
        <div className="border-b px-4 pt-3">
          <TabsList>
            <TabsTrigger value="overview">{t("sessions.skillsList.overview")}</TabsTrigger>
            <TabsTrigger value="files">{t("sessions.skillsList.files")}</TabsTrigger>
            {!readOnly && (
              <TabsTrigger value="settings">{t("sessions.skillsList.settings")}</TabsTrigger>
            )}
          </TabsList>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto p-5">
          <TabsContent value="overview" className="space-y-4">
            <p className="text-sm text-muted-foreground">{skill.description}</p>
            <div className="flex flex-wrap gap-2">
              {files.map((file) => (
                <Button key={file} size="sm" variant="outline" onClick={() => setViewer(file)}>
                  <FileText size={16} />
                  {file}
                </Button>
              ))}
            </div>
          </TabsContent>
          <TabsContent value="files">
            <div className="divide-y divide-border rounded-lg border">
              {files.map((file) => (
                <button
                  key={file}
                  type="button"
                  onClick={() => setViewer(file)}
                  className="block w-full p-3 text-left font-mono text-sm hover:bg-muted"
                >
                  {file}
                </button>
              ))}
            </div>
          </TabsContent>
          {!readOnly && (
            <TabsContent value="settings" className="space-y-6">
              <div className="space-y-2">
                <Label>{t("sessions.skillsList.status")}</Label>
                <ToggleGroup
                  variant="outline"
                  value={[status]}
                  onValueChange={(value: string[]) => value[0] && setStatus(value[0])}
                >
                  {["active", "draft", "deprecated"].map((s) => (
                    <ToggleGroupItem key={s} value={s}>
                      {t(statusLabelKey(s))}
                    </ToggleGroupItem>
                  ))}
                </ToggleGroup>
              </div>
              <div className="space-y-2">
                <Label>{t("sessions.skillsList.description")}</Label>
                <Textarea
                  value={description}
                  onChange={(e) => setDescription((e.target as HTMLTextAreaElement).value)}
                  className="min-h-20"
                />
              </div>
              <div className="flex items-start justify-between gap-4">
                <div className="space-y-0.5">
                  <Label>{t("sessions.skillsList.modelInvocation")}</Label>
                  <p className="text-xs text-muted-foreground">
                    {t("sessions.skillsList.modelInvocationHint")}
                  </p>
                </div>
                <Switch checked={modelEnabled} onCheckedChange={setModelEnabled} />
              </div>
              <Button className="w-full" onClick={() => void save()}>
                {t("common.save")}
              </Button>
              <div className="space-y-2 border-t pt-4">
                <Label className="text-destructive">{t("sessions.skillsList.dangerZone")}</Label>
                <Button
                  variant="outline"
                  className="w-full text-destructive hover:bg-destructive/10"
                  onClick={() => setConfirmOpen(true)}
                >
                  {t("sessions.skillsList.deleteSkill")}
                </Button>
              </div>
            </TabsContent>
          )}
        </div>
      </Tabs>
      {readOnly && (
        <div className="flex items-center gap-2 border-t p-4 text-sm text-muted-foreground">
          <Lock size={16} /> {t("sessions.skillsList.readonlyNote")}
        </div>
      )}
      {viewer && (
        <SkillFileViewer
          agentId={agentId}
          skill={skill}
          path={viewer}
          open
          onOpenChange={(open) => !open && setViewer(null)}
        />
      )}
      <AlertDialog open={confirmOpen} onOpenChange={setConfirmOpen}>
        <AlertDialogPopup>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("sessions.skillsList.deleteConfirm")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("sessions.skillsList.deleteConfirmDesc", { name: skill.name })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogClose render={<Button variant="ghost" />}>
              {t("common.cancel")}
            </AlertDialogClose>
            <Button variant="destructive" onClick={() => void remove()}>
              {t("common.delete")}
            </Button>
          </AlertDialogFooter>
        </AlertDialogPopup>
      </AlertDialog>
    </div>
  );
}

function SkillFileViewer({
  agentId,
  skill,
  path,
  open,
  onOpenChange,
}: {
  agentId: string;
  skill: Skill;
  path: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState("");
  const readOnly = !WRITABLE.has(skill.scope as Scope);
  const file = useQuery({
    queryKey: ["agent-skill-file", agentId, skill.scope, skill.name, path],
    queryFn: async () =>
      (
        await getAgentSkillFile({
          path: { id: agentId, skillId: skill.name },
          query: { scope: skill.scope as Scope, path },
          throwOnError: true,
        })
      ).data,
  });
  const content = editing ? draft : (file.data?.content ?? "");
  useEffect(() => {
    if (file.data?.content != null) setDraft(file.data.content);
  }, [file.data?.content]);
  async function save() {
    await updateAgentSkill({
      path: { id: agentId, skillId: skill.name },
      query: { scope: skill.scope as Scope },
      body: { files: { [path]: draft } },
      throwOnError: true,
    });
    setEditing(false);
    void queryClient.invalidateQueries({
      queryKey: ["agent-skill-file", agentId, skill.scope, skill.name, path],
    });
  }
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogPopup className="max-w-5xl max-sm:h-dvh max-sm:max-w-none">
        <DialogHeader>
          <DialogTitle className="font-mono">{path}</DialogTitle>
          <DialogDescription>{skill.name}</DialogDescription>
        </DialogHeader>
        <DialogPanel className="space-y-3">
          <div className="flex justify-end gap-2">
            <Button
              size="sm"
              variant="ghost"
              onClick={() => void navigator.clipboard.writeText(content)}
            >
              <Copy size={16} />
              {t("common.copy")}
            </Button>
            {!readOnly && (
              <Button size="sm" variant="outline" onClick={() => setEditing((v) => !v)}>
                {t("common.edit")}
              </Button>
            )}
          </div>
          {file.isLoading ? (
            <Spinner />
          ) : editing ? (
            <Textarea
              value={draft}
              onChange={(e) => setDraft((e.target as HTMLTextAreaElement).value)}
              className="min-h-96 font-mono"
            />
          ) : (
            <SkillFilePreview
              path={path}
              content={content}
              emptyText={t("sessions.skillsList.emptyFile")}
            />
          )}
          {editing && <Button onClick={() => void save()}>{t("common.save")}</Button>}
        </DialogPanel>
      </DialogPopup>
    </Dialog>
  );
}

function InstallDialog({
  agentId,
  open,
  onOpenChange,
}: {
  agentId: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const { t } = useI18n();
  const { data: me } = useQuery(meQueryOptions);
  const queryClient = useQueryClient();
  const { showToast } = useToast();
  const [tab, setTab] = useState("clawhub");
  const [q, setQ] = useState("");
  const [scope, setScope] = useState<"user" | "agent">("user");
  const [file, setFile] = useState<File | null>(null);
  const results = useQuery({
    queryKey: ["skill-search", q],
    enabled: q.length > 1,
    queryFn: async () =>
      (await searchSkills({ query: { q }, throwOnError: true })).data?.skills ?? [],
  });
  async function install(source: string) {
    await installAgentSkill({ path: { id: agentId }, body: { source, scope }, throwOnError: true });
    showToast(t("sessions.discover.installSuccess"), "success");
    void queryClient.invalidateQueries({ queryKey: ["agent-skills", agentId] });
  }
  async function upload() {
    if (!file) return;
    await uploadAgentSkill({ path: { id: agentId }, body: { file, scope }, throwOnError: true });
    showToast(t("sessions.discover.installSuccess"), "success");
    void queryClient.invalidateQueries({ queryKey: ["agent-skills", agentId] });
  }
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogPopup>
        <DialogHeader>
          <DialogTitle>{t("sessions.skill.installSkill")}</DialogTitle>
        </DialogHeader>
        <DialogPanel className="space-y-4">
          <Tabs value={tab} onValueChange={setTab}>
            <TabsList>
              <TabsTrigger value="clawhub">ClawHub</TabsTrigger>
              <TabsTrigger value="zip">{t("sessions.skillsList.uploadZip")}</TabsTrigger>
            </TabsList>
            <div className="flex gap-2 pt-3">
              <Button
                size="sm"
                variant={scope === "user" ? "secondary" : "outline"}
                onClick={() => setScope("user")}
              >
                {t("sessions.skillsList.profileScope")}
              </Button>
              {me?.is_admin && (
                <Button
                  size="sm"
                  variant={scope === "agent" ? "secondary" : "outline"}
                  onClick={() => setScope("agent")}
                >
                  {t("sessions.skillsList.agentScope")}
                </Button>
              )}
            </div>
            <TabsContent value="clawhub" className="space-y-3 pt-3">
              <Input
                nativeInput
                value={q}
                onChange={(e) => setQ((e.target as HTMLInputElement).value)}
                placeholder={t("sessions.discover.searchPlaceholder")}
              />
              {(results.data as SkillSearchResult[] | undefined)?.map((r) => (
                <div
                  key={r.id ?? r.name}
                  className="flex items-center justify-between gap-3 rounded-lg border p-3"
                >
                  <span className="font-mono text-sm">{r.name}</span>
                  <Button size="sm" onClick={() => void install(r.source ?? r.name ?? "")}>
                    {t("common.install")}
                  </Button>
                </div>
              ))}
            </TabsContent>
            <TabsContent value="zip" className="space-y-3 pt-3">
              <Input
                nativeInput
                type="file"
                accept=".zip"
                onChange={(e) => setFile((e.target as HTMLInputElement).files?.[0] ?? null)}
              />
              <Button onClick={() => void upload()}>{t("sessions.skillsList.uploadZip")}</Button>
            </TabsContent>
          </Tabs>
        </DialogPanel>
      </DialogPopup>
    </Dialog>
  );
}
