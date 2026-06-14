import { useEffect, useMemo, useState } from "react";
import { useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronRight, Copy, FileText, Lock, Plus, Search, Upload } from "lucide-react";
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
  createAgentSkill,
  deleteAgentSkill,
  getAgentSkill,
  getAgentSkillFile,
  installAgentSkill,
  searchSkills,
  updateAgentSkill,
  uploadAgentSkill,
} from "@/lib/api-client/sdk.gen";
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
import { Collapsible, CollapsiblePanel, CollapsibleTrigger } from "@/components/ui/collapsible";
import {
  Dialog,
  DialogDescription,
  DialogHeader,
  DialogPanel,
  DialogPopup,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Kbd } from "@/components/ui/kbd";
import { Sheet, SheetHeader, SheetPanel, SheetPopup, SheetTitle } from "@/components/ui/sheet";
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
  const [createOpen, setCreateOpen] = useState(false);
  const activeTab = search.tab === "discover" ? "discover" : "installed";
  const params = projectId ? { agentId, projectId } : { agentId };
  const selected =
    search.expand && search.scope
      ? skills.find((s) => s.name === search.expand && s.scope === search.scope)
      : undefined;

  useEffect(() => {
    setHeaderActions(null);
    return () => setHeaderActions(null);
  }, [setHeaderActions]);
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
  const callable = skills.filter((s) => !s.disable_model_invocation).length;
  const readonly = skills.filter((s) => !WRITABLE.has(s.scope as Scope)).length;
  const installedNames = new Set(skills.map((s) => s.name));

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

  return (
    <div className="flex-1 overflow-y-auto">
      <div className="mx-auto max-w-5xl p-4 sm:p-6 lg:p-8">
        <Tabs value={activeTab} onValueChange={setTab}>
          <div className="flex flex-wrap items-center justify-between gap-3 max-md:flex-col max-md:items-stretch">
            <TabsList>
              <TabsTrigger value="installed">
                {t("sessions.skillsList.installedTab")} ({skills.length})
              </TabsTrigger>
              <TabsTrigger value="discover">{t("sessions.skillsList.discoverTab")}</TabsTrigger>
            </TabsList>
            <div className="flex gap-2">
              <Button variant="ghost" onClick={() => setInstallOpen(true)}>
                <Upload size={16} />
                {t("sessions.skillsList.uploadZip")}
              </Button>
              <Button onClick={() => setInstallOpen(true)}>
                {t("sessions.skill.installSkill")}
                <Kbd>⌘K</Kbd>
              </Button>
            </div>
          </div>
          {activeTab === "installed" && (
            <div className="mt-4 flex items-center justify-between gap-4 max-md:block">
              <div className="flex min-w-0 items-center gap-2 max-md:overflow-x-auto">
                <div className="relative w-72 shrink-0 max-md:w-64">
                  <Search
                    className="absolute left-3 top-1/2 -translate-y-1/2 text-muted-foreground"
                    size={16}
                  />
                  <Input
                    nativeInput
                    value={query}
                    onChange={(e) => setQuery((e.target as HTMLInputElement).value)}
                    placeholder={t("sessions.skillsList.searchPlaceholder")}
                    className="pl-9"
                  />
                </div>
                {(["all", ...SCOPES] as const).map((scope) => (
                  <Button
                    key={scope}
                    size="sm"
                    variant={scopeFilter === scope ? "secondary" : "ghost"}
                    onClick={() => setScopeFilter(scope)}
                  >
                    {t(`sessions.skillsList.${scope}`)}{" "}
                    {scope === "all" ? skills.length : counts[scope]}
                  </Button>
                ))}
              </div>
              <p className="hidden text-xs text-muted-foreground md:block">
                {t("sessions.skillsList.stats", { total: skills.length, callable, readonly })}
              </p>
            </div>
          )}
          <TabsContent value="installed" className="mt-6">
            <div className="flex items-start rounded-xl border">
              <div className="min-w-0 flex-1 space-y-6 p-3">
                {isLoading ? (
                  <div className="flex h-48 items-center justify-center">
                    <Spinner />
                  </div>
                ) : (
                  SCOPES.map((scope) => ({
                    scope,
                    items: filtered.filter((s) => s.scope === scope),
                  }))
                    .filter(
                      ({ scope, items }) =>
                        (scopeFilter === "all" || scopeFilter === scope) &&
                        (items.length > 0 || (scope === "user" && !query.trim())),
                    )
                    .map(({ scope, items }) => (
                      <SkillGroup
                        key={scope}
                        scope={scope}
                        skills={items}
                        selected={selected}
                        defaultOpen={scope !== "system"}
                        onSelect={selectSkill}
                        onCreate={scope === "user" ? () => setCreateOpen(true) : undefined}
                      />
                    ))
                )}
              </div>
              {selected && !isMobile && (
                <div className="w-96 shrink-0 self-stretch border-l bg-card">
                  <SkillInspector
                    agentId={agentId}
                    skill={selected}
                    onClose={() => selectSkill()}
                  />
                </div>
              )}
            </div>
          </TabsContent>
          <TabsContent value="discover" className="mt-6">
            <SkillsDiscover agentId={agentId} installedNames={installedNames} />
          </TabsContent>
        </Tabs>
      </div>
      {selected && isMobile && (
        <Sheet open onOpenChange={(open) => !open && selectSkill()}>
          <SheetPopup side="right">
            <SkillInspector
              agentId={agentId}
              skill={selected}
              sheet
              onClose={() => selectSkill()}
            />
          </SheetPopup>
        </Sheet>
      )}
      <InstallDialog agentId={agentId} open={installOpen} onOpenChange={setInstallOpen} />
      <CreateDialog agentId={agentId} open={createOpen} onOpenChange={setCreateOpen} />
      <ToastContainer messages={useToast().toasts} />
    </div>
  );
}

function SkillGroup({
  scope,
  skills,
  selected,
  defaultOpen,
  onSelect,
  onCreate,
}: {
  scope: Scope;
  skills: Skill[];
  selected?: Skill;
  defaultOpen: boolean;
  onSelect: (skill: Skill) => void;
  onCreate?: () => void;
}) {
  const { t } = useI18n();
  return (
    <Collapsible defaultOpen={defaultOpen}>
      <div className="flex items-center justify-between">
        <CollapsibleTrigger className="group flex items-center gap-1.5 text-xs text-muted-foreground">
          <ChevronRight className="size-3.5 transition-transform duration-150 ease-out group-data-[panel-open]:rotate-90" />
          {t(`sessions.skillsList.${scope}`)} · {skills.length}
          {!WRITABLE.has(scope) ? ` · ${t("sessions.skillsList.readonly")}` : ""}
        </CollapsibleTrigger>
        {onCreate && (
          <Button size="sm" variant="ghost" onClick={onCreate}>
            <Plus size={16} />
            {t("sessions.skill.newSkill")}
          </Button>
        )}
      </div>
      <CollapsiblePanel>
        <div className="mt-2 space-y-1">
          {skills.map((skill) => (
            <button
              key={skill.id}
              type="button"
              onClick={() => onSelect(skill)}
              className={cn(
                "w-full rounded-lg px-3 py-2.5 text-left md:flex md:min-h-13 md:items-center md:gap-3 md:py-0",
                selected?.id === skill.id ? "bg-primary/10" : "hover:bg-muted",
              )}
            >
              <div className="flex items-center gap-2 md:min-w-0 md:flex-1 md:gap-3">
                <span className="shrink-0 font-mono text-sm">{skill.name}</span>
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
                <p className="hidden min-w-0 flex-1 truncate text-xs text-muted-foreground md:block">
                  {skill.description}
                </p>
                <span className="ml-auto shrink-0 text-xs text-muted-foreground md:hidden">
                  {formatTime(skill.updated_at)}
                </span>
              </div>
              <p className="mt-0.5 truncate text-xs text-muted-foreground md:hidden">
                {skill.description}
              </p>
              <span className="hidden shrink-0 items-center gap-2 text-xs text-muted-foreground md:flex">
                {!WRITABLE.has(skill.scope as Scope) && <Lock className="size-3.5" />}
                {formatTime(skill.updated_at)}
              </span>
            </button>
          ))}
          {skills.length === 0 && (
            <p className="px-3 py-2 text-xs text-muted-foreground">
              {t("sessions.skillsList.noSkills")}
            </p>
          )}
        </div>
      </CollapsiblePanel>
    </Collapsible>
  );
}

function SkillInspector({
  agentId,
  skill,
  sheet,
  onClose,
}: {
  agentId: string;
  skill: Skill;
  onClose?: () => void;
  sheet?: boolean;
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
      showToast(error instanceof Error ? error.message : t("common.error"), "error");
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
      showToast(error instanceof Error ? error.message : t("common.error"), "error");
    } finally {
      setConfirmOpen(false);
    }
  }
  const body = (
    <>
      <Tabs value={tab} onValueChange={setTab}>
        <TabsList>
          <TabsTrigger value="overview">{t("sessions.skillsList.overview")}</TabsTrigger>
          <TabsTrigger value="files">{t("sessions.skillsList.files")}</TabsTrigger>
          <TabsTrigger value="settings">{t("sessions.skillsList.settings")}</TabsTrigger>
        </TabsList>
        <TabsContent value="overview" className="space-y-4 pt-4">
          <p className="text-sm text-muted-foreground">{skill.description}</p>
          <div className="grid grid-cols-2 gap-3 text-sm">
            <span>{t("sessions.skillsList.scope")}</span>
            <Badge>{t(`sessions.skillsList.${skill.scope}`)}</Badge>
            <span>{t("sessions.skillsList.status")}</span>
            <span>{t(statusLabelKey(skill.status))}</span>
            <span>{t("sessions.skillsList.modelInvocation")}</span>
            <span>
              {skill.disable_model_invocation
                ? t("sessions.skillsList.manual")
                : t("sessions.skillsList.auto")}
            </span>
            <span>{t("sessions.skillsList.fileCount")}</span>
            <span>{files.length}</span>
          </div>
          <div className="flex flex-wrap gap-2">
            {files.map((file) => (
              <Button key={file} size="sm" variant="outline" onClick={() => setViewer(file)}>
                <FileText size={16} />
                {file}
              </Button>
            ))}
          </div>
        </TabsContent>
        <TabsContent value="files" className="pt-4">
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
        <TabsContent value="settings" className="space-y-6 pt-4">
          {readOnly ? (
            <p className="flex items-center gap-2 text-sm text-muted-foreground">
              <Lock size={16} /> {t("sessions.skillsList.readonlyNote")}
            </p>
          ) : (
            <>
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
            </>
          )}
        </TabsContent>
      </Tabs>
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
    </>
  );
  if (sheet)
    return (
      <>
        <SheetHeader>
          <SheetTitle className="font-mono">{skill.name}</SheetTitle>
        </SheetHeader>
        <SheetPanel>{body}</SheetPanel>
      </>
    );
  return (
    <div className="sticky top-0 p-5">
      <div className="mb-4 flex items-center gap-2">
        <span className="font-mono text-sm font-medium">{skill.name}</span>
        <Badge>{t(`sessions.skillsList.${skill.scope}`)}</Badge>
        {skill.status !== "active" && (
          <Badge variant="secondary">{t(statusLabelKey(skill.status))}</Badge>
        )}
      </div>
      {body}
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

function CreateDialog({
  agentId,
  open,
  onOpenChange,
}: {
  agentId: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  async function create() {
    await createAgentSkill({
      path: { id: agentId },
      body: {
        name,
        description,
        scope: "user",
        files: { "SKILL.md": `# ${name}\n\n${description}\n` },
      },
      throwOnError: true,
    });
    void queryClient.invalidateQueries({ queryKey: ["agent-skills", agentId] });
    onOpenChange(false);
  }
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogPopup>
        <DialogHeader>
          <DialogTitle>{t("sessions.skill.newSkill")}</DialogTitle>
        </DialogHeader>
        <DialogPanel className="space-y-3">
          <Input
            nativeInput
            value={name}
            onChange={(e) => setName((e.target as HTMLInputElement).value)}
            placeholder={t("sessions.skillsList.name")}
          />
          <Input
            nativeInput
            value={description}
            onChange={(e) => setDescription((e.target as HTMLInputElement).value)}
            placeholder={t("sessions.skillsList.description")}
          />
          <Button onClick={() => void create()}>{t("common.create")}</Button>
        </DialogPanel>
      </DialogPopup>
    </Dialog>
  );
}
