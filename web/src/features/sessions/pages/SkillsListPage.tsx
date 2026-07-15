import { useEffect, useMemo, useRef, useState, type RefObject } from "react";
import { useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { useInfiniteQuery, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Blocks,
  Check,
  ChevronLeft,
  ChevronRight,
  Clock,
  Copy,
  Download,
  ExternalLink,
  FileText,
  GitBranch,
  GitFork,
  Lock,
  PackageCheck,
  PackagePlus,
  RefreshCw,
  Search,
  Store,
  Trash2,
  Upload,
  X,
} from "lucide-react";
import { useToast, ToastContainer } from "@/hooks/use-toast";
import { useAppShell } from "@/layouts/AppShell";
import {
  agentSkillsInfiniteQueryOptions,
  agentSkillsOptions,
  clawhubSkillDetailOptions,
  clawhubSkillsOptions,
  flattenAgentSkillPages,
} from "@/lib/queries/agents";
import { meQueryOptions } from "@/lib/queries/me";
import { useI18n } from "@/lib/i18n";
import { formatTime } from "@/lib/time";
import { cn } from "@/lib/utils";
import {
  INSTALL_SCOPES,
  isSkillReadOnly,
  SCOPE_DESC_KEY,
  SCOPE_LABEL_KEY,
  type SkillScope,
} from "@/lib/skill-scope";
import type { Skill } from "@/lib/types";
import type { ClawhubSkill } from "@/lib/api-client/types.gen";
import {
  deleteAgentSkill,
  getAgentSkill,
  getAgentSkillFile,
  installAgentSkill,
  updateAgentSkill,
  uploadAgentSkill,
  upgradeAgentSkill,
} from "@/lib/api-client/sdk.gen";
import { apiErrorMessage } from "@/lib/api-error";
import { SkillFilePreview } from "@/features/sessions/SkillFilePreview";
import { MarkdownPreview } from "@/components/MarkdownPreview";
import {
  AlertDialog,
  AlertDialogClose,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogPopup,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty";
import { Label } from "@/components/ui/label";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { Input } from "@/components/ui/input";
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group";
import { Sheet, SheetPopup } from "@/components/ui/sheet";
import { Skeleton } from "@/components/ui/skeleton";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { Tooltip, TooltipPopup, TooltipTrigger } from "@/components/ui/tooltip";

// Filter buckets collapse the two agent scopes (user_agent + system_agent) into a
// single "agent" pill, matching the agent-settings SkillsTab's coarser grouping.
type ScopeFilter = "all" | "system" | "agent" | "user" | "project";
type Source = "installed" | "market" | "manual";
type InstallScope = (typeof INSTALL_SCOPES)[number];
type ToastHandler = (text: string, kind?: "success" | "error") => void;

const SOURCE_META = {
  installed: { icon: PackageCheck, key: "sessions.skillsList.installedTab" },
  market: { icon: Store, key: "sessions.skillsList.market" },
  manual: { icon: PackagePlus, key: "sessions.skillsList.manualTab" },
} as const;

const SCOPE_PILLS: ScopeFilter[] = ["all", "system", "agent", "user", "project"];

// Match FacetTabs' active treatment so the in-page filter pills read as the
// same tab language as the top agent nav (accent pill / muted ghost).
const tabPillCls = (active: boolean, size: "sm" | "xs" = "sm") =>
  cn(
    "inline-flex shrink-0 cursor-pointer items-center gap-1.5 rounded-md font-medium transition-colors",
    size === "sm" ? "h-8 px-3 text-sm" : "h-7 px-2.5 text-xs",
    active
      ? "bg-accent text-accent-foreground"
      : "text-muted-foreground hover:bg-muted/50 hover:text-foreground",
  );

interface SkillsSearch {
  source?: Source;
  fscope?: Exclude<ScopeFilter, "all">;
  sel?: string;
  new?: boolean;
}

function route(projectId?: string) {
  return projectId ? "/agents/$agentId/projects/$projectId/skills" : "/agents/$agentId/skills";
}

function statusLabelKey(status?: string) {
  if (status === "draft") return "sessions.skillsList.statusDraft" as const;
  if (status === "deprecated") return "sessions.skillsList.statusDeprecated" as const;
  return "sessions.skillsList.statusActive" as const;
}

function formatInstalls(n: number): string {
  return n >= 1000 ? `${(n / 1000).toFixed(1).replace(/\.0$/, "")}k` : String(n);
}

// SKILL.md leads with a YAML frontmatter block; left in place markdown renders it as a
// giant setext heading, so drop it before previewing the human-readable body.
function stripFrontmatter(md: string): string {
  const match = md.match(/^\s*---\r?\n[\s\S]*?\r?\n---\r?\n?/);
  return match ? md.slice(match[0].length) : md;
}

// Source is the reliable install key (the slug can differ from the SKILL.md name);
// name/slug are a fallback for skills installed before the source was recorded.
function isSkillInstalled(
  skill: Pick<ClawhubSkill, "name" | "slug">,
  installedNames: Set<string>,
  installedSources: Set<string>,
): boolean {
  return (
    installedSources.has(`clawhub:${skill.slug}`) ||
    installedNames.has(skill.name) ||
    installedNames.has(skill.slug)
  );
}

// A skill can be re-fetched from its source when it was installed from a remote
// (git/github/URL) — clawhub pins and on-disk project skills have no moving ref
// to check, so the upgrade affordance only applies to remote sources.
function isUpdatableSource(source?: string): boolean {
  return (
    !!source && !source.startsWith("clawhub:") && !source.startsWith("/") && !source.startsWith(".")
  );
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

function AuthorChip({ handle, image }: { handle: string; image?: string }) {
  return (
    <span className="inline-flex items-center gap-1.5">
      <Avatar className="size-4">
        {image && <AvatarImage src={image} alt="" />}
        <AvatarFallback className="uppercase">{handle.slice(0, 1)}</AvatarFallback>
      </Avatar>
      {handle}
    </span>
  );
}

// Scope picker for install/upload. Each pill carries a tooltip spelling out what
// the scope means (who it's for, which agents) so the short label needs no manual.
function InstallScopePicker({
  scope,
  onScope,
  showAgentScope,
  size = "xs",
}: {
  scope: InstallScope;
  onScope: (scope: InstallScope) => void;
  showAgentScope: boolean;
  size?: "sm" | "xs";
}) {
  const { t } = useI18n();
  return (
    <div className="flex flex-wrap items-center gap-1">
      {INSTALL_SCOPES.filter((s) => s !== "system_agent" || showAgentScope).map((s) => (
        <Tooltip key={s}>
          <TooltipTrigger
            render={
              <button
                type="button"
                aria-pressed={scope === s}
                onClick={() => onScope(s)}
                className={tabPillCls(scope === s, size)}
              >
                {t(SCOPE_LABEL_KEY[s])}
              </button>
            }
          />
          <TooltipPopup side="top" className="max-w-56">
            {t(SCOPE_DESC_KEY[s])}
          </TooltipPopup>
        </Tooltip>
      ))}
    </div>
  );
}

export function SkillsListPage() {
  const { agentId, projectId } = useParams({ strict: false }) as {
    agentId: string;
    projectId?: string;
  };
  const search = useSearch({ strict: false }) as SkillsSearch;
  const navigate = useNavigate();
  const { t } = useI18n();
  const { setHeaderActions } = useAppShell();
  const { toasts, showToast } = useToast();
  const { data: me } = useQuery(meQueryOptions);

  const source: Source = search.source ?? (search.new ? "manual" : "installed");
  const fscope: ScopeFilter = search.fscope ?? "all";
  const sel = search.sel;
  const params = projectId ? { agentId, projectId } : { agentId };

  const qc = useQueryClient();
  const [query, setQuery] = useState("");
  const [debounced, setDebounced] = useState("");
  const [installScope, setInstallScope] = useState<InstallScope>("user_agent");
  const [installingSlug, setInstallingSlug] = useState<string | null>(null);
  const contentRef = useRef<HTMLDivElement>(null);
  const sentinelRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const id = setTimeout(() => setDebounced(query), 250);
    return () => clearTimeout(id);
  }, [query]);

  const managementSource = source === "installed";
  const management = useInfiniteQuery({
    ...agentSkillsInfiniteQueryOptions({
      agentId,
      projectId,
      ...(fscope !== "all" ? { scopeGroup: fscope } : {}),
      ...(debounced ? { q: debounced } : {}),
    }),
    enabled: managementSource,
  });
  const managedSkills = flattenAgentSkillPages(management.data?.pages) as Skill[];
  const firstManagementPage = management.data?.pages[0];
  const counts = firstManagementPage?.scope_counts;

  // Marketplace needs the complete active set to mark already-installed entries;
  // the management tabs use the paginated query above.
  const installedLookup = useQuery({
    ...agentSkillsOptions(agentId),
    enabled: source === "market",
  });
  const skills = installedLookup.data ?? [];
  const market = useQuery({ ...clawhubSkillsOptions(debounced), enabled: source === "market" });

  useEffect(() => {
    setHeaderActions(null);
    return () => setHeaderActions(null);
  }, [setHeaderActions]);

  const installedNames = useMemo(() => new Set(skills.map((s) => s.name)), [skills]);
  const installedSources = useMemo(
    () => new Set(skills.map((s) => s.source).filter((src): src is string => !!src)),
    [skills],
  );
  const marketRows = market.data ?? [];

  function go(next: Partial<{ source: Source; fscope: ScopeFilter; sel?: string }>) {
    const merged = { source, fscope, sel, ...next };
    const s: SkillsSearch = {};
    if (merged.source !== "installed") s.source = merged.source;
    if (merged.fscope !== "all") s.fscope = merged.fscope;
    if (merged.sel) s.sel = merged.sel;
    void navigate({ to: route(projectId), params, search: s, replace: true });
  }

  // The name fallback preserves the post-install flow until its first paginated refresh.
  const selectedManaged =
    sel && !sel.startsWith("market:")
      ? managedSkills.find((s) => `${s.scope}:${s.id}` === sel || `${s.scope}:${s.name}` === sel)
      : undefined;
  const selectedSlug = sel?.startsWith("market:") ? sel.slice("market:".length) : undefined;
  const selectedRow = selectedSlug ? marketRows.find((r) => r.slug === selectedSlug) : undefined;

  // After a manual install, jump to the Installed tab and open the new skill's
  // drawer so the result is visible — the manual panel itself shows no list.
  function onManualInstalled(scope: InstallScope, name: string) {
    showToast(t("sessions.discover.installSuccess"), "success");
    void qc.invalidateQueries({ queryKey: ["agent-skills", agentId] });
    void qc.invalidateQueries({ queryKey: ["agent-skills-management", agentId] });
    go({ source: "installed", fscope: "all", sel: name ? `${scope}:${name}` : undefined });
  }

  async function install(skill: Pick<ClawhubSkill, "slug" | "name">) {
    setInstallingSlug(skill.slug);
    try {
      await installAgentSkill({
        path: { id: agentId },
        body: { source: `clawhub:${skill.slug}`, scope: installScope },
        throwOnError: true,
      });
      showToast(t("sessions.discover.installSuccess"), "success");
      void qc.invalidateQueries({ queryKey: ["agent-skills", agentId] });
      void qc.invalidateQueries({ queryKey: ["agent-skills-management", agentId] });
    } catch (error) {
      showToast(apiErrorMessage(error, t("common.error")), "error");
    } finally {
      setInstallingSlug(null);
    }
  }

  useEffect(() => {
    if (!managementSource || !management.hasNextPage || management.isFetchingNextPage) return;
    const root = contentRef.current;
    const sentinel = sentinelRef.current;
    if (!root || !sentinel) return;

    let requested = false;
    const loadNext = () => {
      if (requested) return;
      requested = true;
      void management.fetchNextPage();
    };
    const observer = new IntersectionObserver(
      ([entry]) => {
        if (entry?.isIntersecting) loadNext();
      },
      { root, rootMargin: "240px 0px" },
    );
    observer.observe(sentinel);

    // IntersectionObserver does not always emit again when an appended page still
    // leaves the viewport unfilled, so explicitly continue until scrolling is possible.
    const frame = requestAnimationFrame(() => {
      if (root.scrollHeight <= root.clientHeight + 1) loadNext();
    });
    return () => {
      cancelAnimationFrame(frame);
      observer.disconnect();
    };
  }, [
    managementSource,
    managedSkills.length,
    management.hasNextPage,
    management.isFetchingNextPage,
    management.fetchNextPage,
  ]);

  const drawerOpen = !!selectedManaged || !!selectedSlug;

  return (
    <div className="flex h-full min-h-0 flex-col overflow-hidden">
      {/* Filter bar */}
      <div className="flex flex-col gap-2.5 border-b p-3 sm:px-4">
        {/* Row 1: search · source · upload */}
        <div className="flex flex-wrap items-center gap-2">
          {source !== "manual" && (
            <InputGroup className="w-full sm:max-w-xs">
              <InputGroupAddon>
                <Search />
              </InputGroupAddon>
              <InputGroupInput
                nativeInput
                type="search"
                value={query}
                onChange={(e) => setQuery((e.target as HTMLInputElement).value)}
                placeholder={t("sessions.skillsList.searchPlaceholder")}
              />
            </InputGroup>
          )}

          <div className="flex flex-wrap items-center gap-1">
            {(["installed", "market", "manual"] as const).map((s) => {
              const Icon = SOURCE_META[s].icon;
              const active = source === s;
              return (
                <button
                  key={s}
                  type="button"
                  aria-pressed={active}
                  onClick={() => go({ source: s, sel: undefined })}
                  className={tabPillCls(active)}
                >
                  <Icon className="size-4" />
                  {t(SOURCE_META[s].key)}
                </button>
              );
            })}
          </div>
        </div>

        {/* Row 2: lifecycle scope and ownership filters. */}
        {managementSource && (
          <div className="flex flex-wrap items-center gap-1">
            <span className="mr-1 text-xs text-muted-foreground">
              {t("sessions.skillsList.scopeFilter")}
            </span>
            {SCOPE_PILLS.map((scope) => {
              const active = fscope === scope;
              return (
                <button
                  key={scope}
                  type="button"
                  aria-pressed={active}
                  onClick={() => go({ fscope: scope, sel: undefined })}
                  className={tabPillCls(active, "xs")}
                >
                  {t(`sessions.skillsList.${scope}`)}
                  <span className="text-muted-foreground">
                    {scope === "all"
                      ? (counts?.all ?? firstManagementPage?.total_size ?? 0)
                      : (counts?.[scope] ?? 0)}
                  </span>
                </button>
              );
            })}
          </div>
        )}
      </div>

      {/* Content */}
      <div ref={contentRef} className="min-h-0 flex-1 overflow-y-auto p-3 sm:p-4">
        {managementSource && (
          <ManagedSkillsGrid
            query={management}
            skills={managedSkills}
            selectedId={selectedManaged?.id}
            onOpen={(s) => go({ sel: `${s.scope}:${s.id}` })}
            onRetry={() =>
              void (management.isFetchNextPageError
                ? management.fetchNextPage()
                : management.refetch())
            }
            sentinelRef={sentinelRef}
          />
        )}

        {source === "market" && (
          <MarketGrid
            query={market}
            rows={marketRows}
            installedNames={installedNames}
            installedSources={installedSources}
            installingSlug={installingSlug}
            activeSlug={selectedSlug}
            onOpen={(slug) => go({ sel: `market:${slug}` })}
            onInstall={(s) => void install(s)}
          />
        )}

        {source === "manual" && (
          <ManualInstallPanel
            agentId={agentId}
            showAgentScope={!!me?.is_admin}
            notify={showToast}
            onInstalled={onManualInstalled}
          />
        )}
      </div>

      {/* Detail drawer */}
      <Sheet open={drawerOpen} onOpenChange={(open) => !open && go({ sel: undefined })}>
        <SheetPopup
          side="right"
          showCloseButton={false}
          className="w-full sm:w-[560px] sm:max-w-[560px]"
        >
          {selectedManaged ? (
            <SkillInspector
              agentId={agentId}
              skill={selectedManaged}
              notify={showToast}
              onClose={() => go({ sel: undefined })}
            />
          ) : selectedSlug ? (
            <DiscoverDetail
              slug={selectedSlug}
              row={selectedRow}
              installedNames={installedNames}
              installedSources={installedSources}
              installingSlug={installingSlug}
              scope={installScope}
              onScope={setInstallScope}
              showAgentScope={!!me?.is_admin}
              onInstall={(slug) => void install({ slug, name: selectedRow?.name ?? slug })}
              onClose={() => go({ sel: undefined })}
            />
          ) : null}
        </SheetPopup>
      </Sheet>

      <ToastContainer messages={toasts} />
    </div>
  );
}

const GRID_CLS = "grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3";

function ManagedSkillsGrid({
  query,
  skills,
  selectedId,
  onOpen,
  onRetry,
  sentinelRef,
}: {
  query: {
    isLoading: boolean;
    isError: boolean;
    isFetchingNextPage: boolean;
    isFetchNextPageError: boolean;
    hasNextPage: boolean;
  };
  skills: Skill[];
  selectedId?: string;
  onOpen: (skill: Skill) => void;
  onRetry: () => void;
  sentinelRef: RefObject<HTMLDivElement | null>;
}) {
  const { t } = useI18n();
  if (query.isLoading) {
    return (
      <div className="flex h-48 items-center justify-center">
        <Spinner />
      </div>
    );
  }
  if (query.isError && skills.length === 0) {
    return (
      <Empty>
        <EmptyHeader>
          <EmptyMedia variant="icon">
            <Blocks />
          </EmptyMedia>
          <EmptyTitle>{t("sessions.skillsList.loadError")}</EmptyTitle>
          <EmptyDescription>{t("sessions.skillsList.loadErrorDesc")}</EmptyDescription>
        </EmptyHeader>
        <Button variant="outline" onClick={onRetry}>
          <RefreshCw />
          {t("common.retry")}
        </Button>
      </Empty>
    );
  }
  if (skills.length === 0) {
    return (
      <p className="px-3 py-8 text-center text-sm text-muted-foreground">
        {t("sessions.skillsList.noSkills")}
      </p>
    );
  }
  return (
    <>
      <div className={GRID_CLS}>
        {skills.map((skill) => (
          <ManagedSkillCard
            key={skill.id}
            skill={skill}
            active={selectedId === skill.id}
            onOpen={() => onOpen(skill)}
          />
        ))}
      </div>
      <div ref={sentinelRef} className="flex min-h-12 items-center justify-center py-3">
        {query.isFetchingNextPage && <Spinner />}
        {query.isFetchNextPageError && (
          <Button variant="outline" size="sm" onClick={onRetry}>
            <RefreshCw />
            {t("common.retry")}
          </Button>
        )}
        {!query.hasNextPage && !query.isFetchNextPageError && (
          <span className="text-xs text-muted-foreground">
            {t("sessions.skillsList.allLoaded")}
          </span>
        )}
      </div>
    </>
  );
}

function ManagedSkillCard({
  skill,
  active,
  onOpen,
}: {
  skill: Skill;
  active: boolean;
  onOpen: () => void;
}) {
  const { t } = useI18n();
  const sourceLabel = t(skillSourceMessageKey(skill));
  return (
    <button
      type="button"
      onClick={onOpen}
      className={cn(
        "flex flex-col gap-3 rounded-lg border bg-card p-4 text-left transition-colors",
        active ? "border-primary/40 bg-accent" : "border-border hover:bg-muted/40",
      )}
    >
      <div className="flex items-start gap-3">
        <SkillGlyph />
        <div className="flex min-w-0 flex-1 flex-wrap items-center gap-1.5">
          <span className="truncate font-mono text-sm font-medium">{skill.name}</span>
          {skill.version && (
            <Badge variant="outline" size="sm">
              {skill.version}
            </Badge>
          )}
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
      </div>
      <p className="line-clamp-2 min-h-9 text-xs text-muted-foreground">{skill.description}</p>
      <div className="mt-auto flex flex-wrap items-center gap-2 border-t pt-3 text-xs text-muted-foreground">
        <Badge variant="outline" size="sm">
          {t(SCOPE_LABEL_KEY[skill.scope as SkillScope])}
        </Badge>
        <span className="ml-auto">{sourceLabel}</span>
      </div>
    </button>
  );
}

function skillSourceMessageKey(skill: Skill) {
  if (skill.scope === "system") return "sessions.skillsList.builtin" as const;
  if (skill.created_by === "reflect") return "sessions.skillsList.generated" as const;
  return "sessions.skillsList.manualMaintenance" as const;
}

function MarketGrid({
  query,
  rows,
  installedNames,
  installedSources,
  installingSlug,
  activeSlug,
  onOpen,
  onInstall,
}: {
  query: { isLoading: boolean; isError: boolean };
  rows: ClawhubSkill[];
  installedNames: Set<string>;
  installedSources: Set<string>;
  installingSlug: string | null;
  activeSlug?: string;
  onOpen: (slug: string) => void;
  onInstall: (skill: Pick<ClawhubSkill, "slug" | "name">) => void;
}) {
  const { t } = useI18n();
  if (query.isLoading) {
    return (
      <div className={GRID_CLS}>
        {Array.from({ length: 6 }).map((_, i) => (
          <div key={i} className="space-y-3 rounded-lg border p-4">
            <div className="flex items-center gap-3">
              <Skeleton className="size-9 rounded-lg" />
              <Skeleton className="h-4 w-32" />
            </div>
            <Skeleton className="h-3 w-full" />
            <Skeleton className="h-3 w-4/5" />
          </div>
        ))}
      </div>
    );
  }
  if (query.isError || rows.length === 0) {
    return (
      <Empty>
        <EmptyHeader>
          <EmptyMedia variant="icon">
            <Blocks />
          </EmptyMedia>
          <EmptyTitle>{t("sessions.discover.emptyTitle")}</EmptyTitle>
          <EmptyDescription>
            {query.isError ? t("sessions.discover.loadError") : t("sessions.discover.empty")}
          </EmptyDescription>
        </EmptyHeader>
      </Empty>
    );
  }
  return (
    <div className={GRID_CLS}>
      {rows.map((skill) => (
        <MarketCard
          key={skill.slug}
          skill={skill}
          active={skill.slug === activeSlug}
          installed={isSkillInstalled(skill, installedNames, installedSources)}
          installing={installingSlug === skill.slug}
          installDisabled={installingSlug !== null}
          onOpen={() => onOpen(skill.slug)}
          onInstall={() => onInstall(skill)}
        />
      ))}
    </div>
  );
}

function MarketCard({
  skill,
  active,
  installed,
  installing,
  installDisabled,
  onOpen,
  onInstall,
}: {
  skill: ClawhubSkill;
  active: boolean;
  installed: boolean;
  installing: boolean;
  installDisabled: boolean;
  onOpen: () => void;
  onInstall: () => void;
}) {
  const { t } = useI18n();
  const count = skill.installs ?? skill.downloads;
  return (
    <button
      type="button"
      onClick={onOpen}
      className={cn(
        "flex flex-col gap-3 rounded-lg border bg-card p-4 text-left transition-colors",
        active ? "border-primary/40 bg-accent" : "border-border hover:bg-muted/40",
      )}
    >
      <div className="flex items-start gap-3">
        <SkillGlyph />
        <div className="flex min-w-0 flex-1 items-center gap-2">
          <span className="truncate font-mono text-sm font-medium">{skill.name}</span>
          {skill.version && (
            <Badge variant="outline" size="sm">
              v{skill.version}
            </Badge>
          )}
        </div>
        {installed && (
          <Badge variant="success" size="sm">
            <Check />
          </Badge>
        )}
      </div>
      {skill.summary && (
        <p className="line-clamp-2 min-h-9 text-xs text-muted-foreground">{skill.summary}</p>
      )}
      <div className="mt-auto flex items-center gap-3 border-t pt-3 text-xs text-muted-foreground">
        {count != null && (
          <span className="inline-flex items-center gap-1">
            <Download className="size-4" />
            {formatInstalls(count)}
          </span>
        )}
        {skill.author_handle && (
          <AuthorChip handle={skill.author_handle} image={skill.author_image} />
        )}
        <span className="ml-auto" onClick={(e) => e.stopPropagation()}>
          {installed ? (
            <Button size="xs" variant="ghost" disabled>
              {t("sessions.discover.installed")}
            </Button>
          ) : (
            <Button size="xs" loading={installing} disabled={installDisabled} onClick={onInstall}>
              {t("common.install")}
            </Button>
          )}
        </span>
      </div>
    </button>
  );
}

function SkillInspector({
  agentId,
  skill,
  notify,
  onClose,
}: {
  agentId: string;
  skill: Skill;
  notify: ToastHandler;
  onClose: () => void;
}) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { data: me } = useQuery(meQueryOptions);
  const [description, setDescription] = useState(skill.description ?? "");
  const [version, setVersion] = useState(skill.version ?? "");
  const [status, setStatus] = useState(skill.status ?? "active");
  const [modelEnabled, setModelEnabled] = useState(!skill.disable_model_invocation);
  const [viewer, setViewer] = useState<string | null>(null);
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [upgrading, setUpgrading] = useState(false);
  const [convertToManual, setConvertToManual] = useState(false);
  const scopeReadOnly = isSkillReadOnly(skill.scope, !!me?.is_admin);
  const readOnly = scopeReadOnly;
  const canUpgrade = !readOnly && isUpdatableSource(skill.source);
  const detail = useQuery({
    queryKey: ["agent-skill", agentId, skill.scope, skill.id],
    queryFn: async () =>
      (
        await getAgentSkill({
          path: { id: agentId, skillId: skill.id },
          query: { scope: skill.scope as SkillScope },
          throwOnError: true,
        })
      ).data as Skill,
  });
  const files = detail.data?.files ?? skill.files ?? [];

  useEffect(() => {
    setDescription(skill.description ?? "");
    setVersion(skill.version ?? "");
    setStatus(skill.status ?? "active");
    setModelEnabled(!skill.disable_model_invocation);
    setConvertToManual(false);
    setViewer(null);
  }, [skill]);

  function invalidateSkillQueries() {
    void queryClient.invalidateQueries({ queryKey: ["agent-skills", agentId] });
    void queryClient.invalidateQueries({ queryKey: ["agent-skills-management", agentId] });
    void queryClient.invalidateQueries({
      queryKey: ["agent-skill", agentId, skill.scope, skill.id],
    });
  }

  async function save() {
    // Keep the conversion decision stable while local form state is reset after saving.
    const shouldConvertToManual = convertToManual;
    try {
      await updateAgentSkill({
        path: { id: agentId, skillId: skill.id },
        query: { scope: skill.scope as SkillScope },
        body: {
          description,
          status,
          disable_model_invocation: !modelEnabled,
          version,
          ...(shouldConvertToManual ? { convert_to_manual: true } : {}),
        },
        throwOnError: true,
      });
      notify(t("sessions.skillsList.saved"), "success");
      invalidateSkillQueries();
      if (shouldConvertToManual) onClose();
    } catch (error) {
      notify(apiErrorMessage(error, t("common.error")), "error");
    }
  }
  async function upgrade() {
    setUpgrading(true);
    try {
      const res = await upgradeAgentSkill({
        path: { id: agentId, skillId: skill.id },
        query: { scope: skill.scope as SkillScope },
        throwOnError: true,
      });
      if (res.data?.updated) {
        notify(
          t("sessions.skillsList.upgradeDone", { version: res.data.version ?? "" }),
          "success",
        );
        invalidateSkillQueries();
      } else {
        notify(t("sessions.skillsList.upgradeUpToDate"), "success");
      }
    } catch (error) {
      notify(apiErrorMessage(error, t("common.error")), "error");
    } finally {
      setUpgrading(false);
    }
  }
  async function remove() {
    try {
      await deleteAgentSkill({
        path: { id: agentId, skillId: skill.id },
        query: { scope: skill.scope as SkillScope },
        throwOnError: true,
      });
      notify(t("sessions.skillsList.deletedSuccess"), "success");
      await queryClient.invalidateQueries({ queryKey: ["agent-skills-management", agentId] });
      void queryClient.invalidateQueries({ queryKey: ["agent-skills", agentId] });
      onClose();
    } catch (error) {
      notify(apiErrorMessage(error, t("common.error")), "error");
    } finally {
      setConfirmOpen(false);
    }
  }

  // Drilling into a file swaps the whole drawer to a file view rather than
  // stacking a centered dialog over the drawer (the old behaviour read as broken).
  if (viewer) {
    return (
      <SkillFileView
        agentId={agentId}
        skill={skill}
        path={viewer}
        readOnly={readOnly}
        notify={notify}
        onBack={() => setViewer(null)}
        onClose={onClose}
      />
    );
  }

  return (
    <div className="flex h-full min-h-0 flex-1 flex-col">
      <div className="flex items-start gap-3 border-b p-5">
        <SkillGlyph className="size-11 rounded-lg" />
        <div className="min-w-0 flex-1">
          <h2 className="truncate font-mono text-base font-semibold">{skill.name}</h2>
          <div className="mt-2 flex flex-wrap items-center gap-2">
            <Tooltip>
              <TooltipTrigger render={<Badge variant="secondary" size="sm" />}>
                {t(SCOPE_LABEL_KEY[skill.scope as SkillScope])}
              </TooltipTrigger>
              <TooltipPopup side="bottom" className="max-w-56">
                {t(SCOPE_DESC_KEY[skill.scope as SkillScope])}
              </TooltipPopup>
            </Tooltip>
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
            <Badge variant="outline" size="sm">
              {t(skillSourceMessageKey(skill))}
            </Badge>
          </div>
          <div className="mt-2 flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground">
            {skill.scope !== "system" && (
              <span className="inline-flex items-center gap-1">
                <Clock className="size-4" />
                {formatTime(skill.updated_at)}
              </span>
            )}
            {skill.source && (
              <span className="inline-flex items-center gap-1 font-mono">
                <GitBranch className="size-4" />
                {skill.source}
                {skill.version && (
                  <Badge variant="outline" size="sm">
                    {skill.version}
                  </Badge>
                )}
              </span>
            )}
          </div>
        </div>
        <Button size="icon-sm" variant="ghost" aria-label={t("common.close")} onClick={onClose}>
          <X size={16} />
        </Button>
      </div>

      <div className="min-h-0 flex-1 space-y-6 overflow-y-auto p-5">
        <section className="space-y-2">
          <Label>{t("sessions.skillsList.description")}</Label>
          {readOnly ? (
            <p className="text-sm text-muted-foreground">
              {skill.description || t("sessions.skillsList.emptyFile")}
            </p>
          ) : (
            <Textarea
              value={description}
              onChange={(e) => setDescription((e.target as HTMLTextAreaElement).value)}
              className="min-h-20"
            />
          )}
        </section>

        <section className="space-y-2">
          <Label>
            {t("sessions.skillsList.files")} · {files.length}
          </Label>
          <div className="divide-y divide-border overflow-hidden rounded-lg border">
            {files.map((file) => (
              <button
                key={file}
                type="button"
                onClick={() => setViewer(file)}
                className="flex w-full items-center gap-2 p-3 text-left font-mono text-sm hover:bg-muted"
              >
                <FileText className="size-4 shrink-0 text-muted-foreground" />
                <span className="truncate">{file}</span>
                <ChevronRight className="ml-auto size-4 shrink-0 text-muted-foreground" />
              </button>
            ))}
          </div>
        </section>

        {!readOnly && (
          <section className="space-y-4">
            <div className="space-y-2">
              <Label>{t("sessions.skillsList.status")}</Label>
              <ToggleGroup
                variant="outline"
                value={[status]}
                onValueChange={(value: string[]) => value[0] && setStatus(value[0])}
              >
                {["active", "draft"].map((s) => (
                  <ToggleGroupItem key={s} value={s}>
                    {t(statusLabelKey(s))}
                  </ToggleGroupItem>
                ))}
              </ToggleGroup>
            </div>
            <div className="space-y-2">
              <Label>{t("sessions.skillsList.versionLabel")}</Label>
              <Input
                value={version}
                onChange={(e) => setVersion((e.target as HTMLInputElement).value)}
                className="font-mono"
              />
              <p className="text-xs text-muted-foreground">
                {t("sessions.skillsList.versionHint")}
              </p>
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
            {skill.created_by === "reflect" && (
              <div className="space-y-2 rounded-lg border p-3">
                <div className="flex items-start justify-between gap-4">
                  <div className="space-y-0.5">
                    <Label>{t("sessions.skillsList.convertToManual")}</Label>
                    <p className="text-xs text-muted-foreground">
                      {t("sessions.skillsList.convertToManualHint")}
                    </p>
                  </div>
                  <Switch checked={convertToManual} onCheckedChange={setConvertToManual} />
                </div>
                {convertToManual && (
                  <p className="text-xs text-destructive">
                    {t("sessions.skillsList.convertToManualWarning")}
                  </p>
                )}
              </div>
            )}
          </section>
        )}
      </div>

      {readOnly ? (
        <div className="flex items-center gap-2 border-t p-4 text-sm text-muted-foreground">
          <Lock size={16} /> {t("sessions.skillsList.readonlyNote")}
        </div>
      ) : (
        <div className="flex items-center gap-2 border-t p-4">
          <Button variant="destructive-outline" onClick={() => setConfirmOpen(true)}>
            <Trash2 size={16} />
            {t("sessions.skillsList.deleteSkill")}
          </Button>
          <div className="ml-auto flex items-center gap-2">
            {canUpgrade && (
              <Button variant="outline" loading={upgrading} onClick={() => void upgrade()}>
                <RefreshCw size={16} />
                {t("sessions.skillsList.upgradeCheck")}
              </Button>
            )}
            <Button onClick={() => void save()}>{t("common.save")}</Button>
          </div>
        </div>
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
              {t("sessions.skillsList.deleteSkill")}
            </Button>
          </AlertDialogFooter>
        </AlertDialogPopup>
      </AlertDialog>
    </div>
  );
}

// Inline file view: replaces the drawer body so reading/editing a skill file
// stays on the same surface instead of opening a separate centered dialog.
function SkillFileView({
  agentId,
  skill,
  path,
  readOnly,
  notify,
  onBack,
  onClose,
}: {
  agentId: string;
  skill: Skill;
  path: string;
  readOnly: boolean;
  notify: ToastHandler;
  onBack: () => void;
  onClose: () => void;
}) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState("");
  const [convertToManual, setConvertToManual] = useState(false);
  const file = useQuery({
    queryKey: ["agent-skill-file", agentId, skill.scope, skill.id, path],
    queryFn: async () =>
      (
        await getAgentSkillFile({
          path: { id: agentId, skillId: skill.id },
          query: { scope: skill.scope as SkillScope, path },
          throwOnError: true,
        })
      ).data,
  });
  const content = editing ? draft : (file.data?.content ?? "");
  useEffect(() => {
    if (file.data?.content != null) setDraft(file.data.content);
  }, [file.data?.content]);
  async function save() {
    // Keep the conversion decision stable while local editor state is reset after saving.
    const shouldConvertToManual = convertToManual;
    try {
      await updateAgentSkill({
        path: { id: agentId, skillId: skill.id },
        query: { scope: skill.scope as SkillScope },
        body: {
          files: { [path]: draft },
          ...(shouldConvertToManual ? { convert_to_manual: true } : {}),
        },
        throwOnError: true,
      });
      setEditing(false);
      setConvertToManual(false);
      notify(t("sessions.skillsList.saved"), "success");
      void queryClient.invalidateQueries({
        queryKey: ["agent-skill-file", agentId, skill.scope, skill.id, path],
      });
      void queryClient.invalidateQueries({ queryKey: ["agent-skills", agentId] });
      void queryClient.invalidateQueries({ queryKey: ["agent-skills-management", agentId] });
      if (shouldConvertToManual) onClose();
    } catch (error) {
      notify(apiErrorMessage(error, t("common.error")), "error");
    }
  }
  return (
    <div className="flex h-full min-h-0 flex-1 flex-col">
      <div className="flex items-center gap-2 border-b p-4">
        <Button size="icon-sm" variant="ghost" aria-label={t("common.back")} onClick={onBack}>
          <ChevronLeft size={16} />
        </Button>
        <div className="min-w-0 flex-1">
          <p className="truncate font-mono text-sm font-medium">{path}</p>
          <p className="truncate font-mono text-xs text-muted-foreground">{skill.name}</p>
        </div>
        <Button
          size="sm"
          variant="ghost"
          onClick={() => void navigator.clipboard.writeText(content)}
        >
          <Copy size={16} />
          <span className="max-sm:hidden">{t("common.copy")}</span>
        </Button>
        {!readOnly && (
          <Button
            size="sm"
            variant="outline"
            onClick={() => {
              setEditing((value) => !value);
              if (editing) setConvertToManual(false);
            }}
          >
            {editing ? t("common.cancel") : t("common.edit")}
          </Button>
        )}
        <Button size="icon-sm" variant="ghost" aria-label={t("common.close")} onClick={onClose}>
          <X size={16} />
        </Button>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto p-5">
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
      </div>
      {editing && (
        <div className="space-y-3 border-t p-4">
          {skill.created_by === "reflect" && (
            <div className="space-y-2 rounded-lg border p-3">
              <div className="flex items-start justify-between gap-4">
                <div className="space-y-0.5">
                  <Label>{t("sessions.skillsList.convertToManual")}</Label>
                  <p className="text-xs text-muted-foreground">
                    {t("sessions.skillsList.convertToManualHint")}
                  </p>
                </div>
                <Switch checked={convertToManual} onCheckedChange={setConvertToManual} />
              </div>
              {convertToManual && (
                <p className="text-xs text-destructive">
                  {t("sessions.skillsList.convertToManualWarning")}
                </p>
              )}
            </div>
          )}
          <Button className="w-full" onClick={() => void save()}>
            {t("common.save")}
          </Button>
        </div>
      )}
    </div>
  );
}

function DiscoverDetail({
  slug,
  row,
  installedNames,
  installedSources,
  installingSlug,
  scope,
  onScope,
  showAgentScope,
  onInstall,
  onClose,
}: {
  slug: string;
  row?: ClawhubSkill;
  installedNames: Set<string>;
  installedSources: Set<string>;
  installingSlug: string | null;
  scope: InstallScope;
  onScope: (scope: InstallScope) => void;
  showAgentScope: boolean;
  onInstall: (slug: string) => void;
  onClose: () => void;
}) {
  const { t } = useI18n();
  const { data, isLoading, isError } = useQuery(clawhubSkillDetailOptions(slug));
  const name = data?.name ?? row?.name ?? slug;
  const version = data?.version ?? row?.version;
  const summary = data?.summary ?? row?.summary;
  const count = row?.installs ?? row?.downloads;
  const installed = isSkillInstalled({ name, slug }, installedNames, installedSources);
  const readme = stripFrontmatter(data?.readme ?? "").trim();
  const files = data?.files ?? [];

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="border-b p-5">
        <div className="flex items-start gap-3">
          <SkillGlyph className="size-11 rounded-lg" />
          <div className="min-w-0 flex-1">
            <h2 className="truncate font-mono text-base font-semibold">{name}</h2>
            <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
              {version && (
                <Badge variant="outline" size="sm">
                  v{version}
                </Badge>
              )}
              {count != null && (
                <span className="inline-flex items-center gap-1">
                  <Download className="size-4" />
                  {t("sessions.discover.installs", { n: formatInstalls(count) })}
                </span>
              )}
              {row?.author_handle && (
                <AuthorChip handle={row.author_handle} image={row.author_image} />
              )}
              {row?.updated_at && (
                <span className="inline-flex items-center gap-1">
                  <Clock className="size-4" />
                  {t("sessions.discover.updated", { t: formatTime(row.updated_at) })}
                </span>
              )}
            </div>
          </div>
          <Button size="icon-sm" variant="ghost" aria-label={t("common.close")} onClick={onClose}>
            <X size={16} />
          </Button>
        </div>
        {summary && <p className="mt-3 text-sm text-muted-foreground">{summary}</p>}
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto p-5">
        {files.length > 0 && (
          <div className="mb-5 space-y-2">
            <p className="text-xs font-medium text-muted-foreground">
              {t("sessions.discover.files")} · {files.length}
            </p>
            <div className="flex flex-wrap gap-1.5">
              {files.map((file) => (
                <span
                  key={file}
                  className="inline-flex items-center gap-1 rounded-md bg-muted px-2 py-1 font-mono text-xs text-muted-foreground"
                >
                  <FileText className="size-4" />
                  {file}
                </span>
              ))}
            </div>
          </div>
        )}
        <p className="mb-2 text-xs font-medium text-muted-foreground">
          {t("sessions.discover.readme")}
        </p>
        {isLoading ? (
          <div className="space-y-2.5">
            <Skeleton className="h-4 w-1/3" />
            <Skeleton className="h-3 w-full" />
            <Skeleton className="h-3 w-full" />
            <Skeleton className="h-3 w-4/5" />
            <Skeleton className="mt-4 h-3 w-full" />
            <Skeleton className="h-3 w-2/3" />
          </div>
        ) : isError ? (
          <p className="text-sm text-muted-foreground">{t("sessions.discover.loadError")}</p>
        ) : readme ? (
          <MarkdownPreview content={readme} />
        ) : (
          <p className="text-sm italic text-muted-foreground">
            {t("sessions.skillsList.emptyFile")}
          </p>
        )}
      </div>
      <div className="space-y-3 border-t p-4">
        {!installed && (
          <div className="space-y-1.5">
            <span className="text-xs text-muted-foreground">
              {t("sessions.discover.installTo")}
            </span>
            <InstallScopePicker scope={scope} onScope={onScope} showAgentScope={showAgentScope} />
          </div>
        )}
        <div className="flex items-center gap-2">
          {installed && (
            <Badge variant="success">
              <Check />
              {t("sessions.discover.installed")}
            </Badge>
          )}
          <span className="ml-auto inline-flex items-center gap-2">
            {row?.slug && (
              <Button
                size="sm"
                variant="ghost"
                render={
                  <a
                    href={`https://clawhub.ai/skills/${row.slug}`}
                    target="_blank"
                    rel="noreferrer"
                  />
                }
              >
                <ExternalLink size={16} />
                ClawHub
              </Button>
            )}
            {!installed && (
              <Button
                size="sm"
                loading={installingSlug === slug}
                disabled={installingSlug !== null}
                onClick={() => onInstall(slug)}
              >
                <Download size={16} />
                {t("common.install")}
              </Button>
            )}
          </span>
        </div>
      </div>
    </div>
  );
}

// Build an mcphub shorthand source from a GitHub repo, the skill to select inside
// it, and an optional version. repo may be "owner/repo" or any github.com URL; we
// reduce it to "owner/repo", append "@<skill>" so a multi-skill repo resolves to
// one skill, and "#<version>" to pin a tag/branch — e.g. "owner/repo@foo#v1.2.0".
function githubSource(repo: string, skill: string, version: string): string {
  const r = repo.trim();
  const s = skill.trim();
  const v = version.trim();
  const m = r.match(/(?:github\.com[/:])?([^/\s]+\/[^/\s@#?]+?)(?:\.git)?(?:[/@#?].*)?$/i);
  let out = m ? m[1] : r;
  if (s) out += `@${s}`;
  if (v) out += `#${v}`;
  return out;
}

// ManualInstallPanel is the inline "manual install" tab: install a skill by
// pointing at a GitHub repo or uploading a ZIP. It replaces the former modal
// dialogs and shares one install-scope picker across both methods. onInstalled
// reports the chosen scope and the installed skill name so the page can reveal
// the result.
function ManualInstallPanel({
  agentId,
  showAgentScope,
  notify,
  onInstalled,
}: {
  agentId: string;
  showAgentScope: boolean;
  notify: ToastHandler;
  onInstalled: (scope: InstallScope, name: string) => void;
}) {
  const { t } = useI18n();
  const [scope, setScope] = useState<InstallScope>("user_agent");

  function onDone(name: string) {
    onInstalled(scope, name);
  }
  function onError(error: unknown) {
    notify(apiErrorMessage(error, t("common.error")), "error");
  }

  return (
    <div className="mx-auto max-w-xl space-y-5">
      <div className="space-y-1.5">
        <span className="text-xs text-muted-foreground">{t("sessions.discover.installTo")}</span>
        <InstallScopePicker
          scope={scope}
          onScope={setScope}
          showAgentScope={showAgentScope}
          size="sm"
        />
      </div>
      <GitHubInstallCard agentId={agentId} scope={scope} onInstalled={onDone} onError={onError} />
      <ZipUploadCard agentId={agentId} scope={scope} onInstalled={onDone} onError={onError} />
    </div>
  );
}

function GitHubInstallCard({
  agentId,
  scope,
  onInstalled,
  onError,
}: {
  agentId: string;
  scope: InstallScope;
  onInstalled: (name: string) => void;
  onError: (error: unknown) => void;
}) {
  const { t } = useI18n();
  const [repo, setRepo] = useState("");
  const [skill, setSkill] = useState("");
  const [version, setVersion] = useState("");
  const [busy, setBusy] = useState(false);
  const ready = repo.trim() !== "" && skill.trim() !== "";

  async function install() {
    if (!ready) return;
    setBusy(true);
    try {
      const res = await installAgentSkill({
        path: { id: agentId },
        body: { source: githubSource(repo, skill, version), scope },
        throwOnError: true,
      });
      onInstalled(res.data?.name ?? skill.trim());
      setRepo("");
      setSkill("");
      setVersion("");
    } catch (error) {
      onError(error);
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="space-y-3 rounded-lg border p-4">
      <div className="flex items-center gap-2 text-sm font-medium">
        <GitFork className="size-4" />
        {t("sessions.skillsList.installGithub")}
      </div>
      <div className="space-y-1.5">
        <Label>{t("sessions.skillsList.githubRepo")}</Label>
        <Input
          nativeInput
          autoComplete="off"
          value={repo}
          onChange={(e) => setRepo((e.target as HTMLInputElement).value)}
          placeholder={t("sessions.skillsList.githubRepoPlaceholder")}
        />
      </div>
      <div className="space-y-1.5">
        <Label>{t("sessions.skillsList.githubSkill")}</Label>
        <Input
          nativeInput
          autoComplete="off"
          value={skill}
          onChange={(e) => setSkill((e.target as HTMLInputElement).value)}
          placeholder={t("sessions.skillsList.githubSkillPlaceholder")}
        />
      </div>
      <div className="space-y-1.5">
        <Label>{t("sessions.skillsList.githubVersion")}</Label>
        <Input
          nativeInput
          autoComplete="off"
          value={version}
          onChange={(e) => setVersion((e.target as HTMLInputElement).value)}
          placeholder={t("sessions.skillsList.githubVersionPlaceholder")}
        />
      </div>
      <p className="text-xs text-muted-foreground">{t("sessions.skillsList.githubHint")}</p>
      <Button disabled={!ready || busy} loading={busy} onClick={() => void install()}>
        <GitFork size={16} />
        {t("common.install")}
      </Button>
    </section>
  );
}

function ZipUploadCard({
  agentId,
  scope,
  onInstalled,
  onError,
}: {
  agentId: string;
  scope: InstallScope;
  onInstalled: (name: string) => void;
  onError: (error: unknown) => void;
}) {
  const { t } = useI18n();
  const [file, setFile] = useState<File | null>(null);
  const [busy, setBusy] = useState(false);

  async function upload() {
    if (!file) return;
    setBusy(true);
    try {
      const res = await uploadAgentSkill({
        path: { id: agentId },
        body: { file, scope },
        throwOnError: true,
      });
      onInstalled(res.data?.name ?? "");
      setFile(null);
    } catch (error) {
      onError(error);
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="space-y-3 rounded-lg border p-4">
      <div className="flex items-center gap-2 text-sm font-medium">
        <Upload className="size-4" />
        {t("sessions.skillsList.uploadZip")}
      </div>
      <Input
        nativeInput
        type="file"
        accept=".zip"
        onChange={(e) => setFile((e.target as HTMLInputElement).files?.[0] ?? null)}
      />
      <Button disabled={!file || busy} loading={busy} onClick={() => void upload()}>
        <Upload size={16} />
        {t("sessions.skillsList.uploadZip")}
      </Button>
    </section>
  );
}
