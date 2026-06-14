import { useEffect, useState } from "react";
import { useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Blocks, Check, Clock, Download, ExternalLink, FileText, Search, X } from "lucide-react";
import { useToast, ToastContainer } from "@/hooks/use-toast";
import { useIsMobile } from "@/hooks/use-mobile";
import { installAgentSkill } from "@/lib/api-client/sdk.gen";
import type { ClawhubSkill } from "@/lib/api-client/types.gen";
import { clawhubSkillDetailOptions, clawhubSkillsOptions } from "@/lib/queries/agents";
import { meQueryOptions } from "@/lib/queries/me";
import { apiErrorMessage } from "@/lib/api-error";
import { useI18n } from "@/lib/i18n";
import { formatTime } from "@/lib/time";
import { cn } from "@/lib/utils";
import { MarkdownPreview } from "@/components/MarkdownPreview";
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
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group";
import { Sheet, SheetPanel, SheetPopup } from "@/components/ui/sheet";
import { Skeleton } from "@/components/ui/skeleton";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";

type Scope = "user" | "agent";

function formatInstalls(n: number): string {
  return n >= 1000 ? `${(n / 1000).toFixed(1).replace(/\.0$/, "")}k` : String(n);
}

// SKILL.md leads with a YAML frontmatter block; left in place markdown renders it as a
// giant setext heading, so drop it before previewing the human-readable body.
function stripFrontmatter(md: string): string {
  const match = md.match(/^\s*---\r?\n[\s\S]*?\r?\n---\r?\n?/);
  return match ? md.slice(match[0].length) : md;
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

// isSkillInstalled matches a marketplace row against installed skills. Source is the
// reliable key (the slug can differ from the SKILL.md frontmatter name); name/slug are
// a fallback for skills installed before the source was recorded.
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

export function SkillsDiscover({
  agentId,
  installedNames,
  installedSources,
}: {
  agentId: string;
  installedNames: Set<string>;
  installedSources: Set<string>;
}) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { toasts, showToast } = useToast();
  const isMobile = useIsMobile();
  const { projectId } = useParams({ strict: false }) as { projectId?: string };
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as { dslug?: string };
  const { data: me } = useQuery(meQueryOptions);
  const [query, setQuery] = useState("");
  const [debounced, setDebounced] = useState("");
  const [installingSlug, setInstallingSlug] = useState<string | null>(null);
  const [scope, setScope] = useState<Scope>("user");

  useEffect(() => {
    const id = setTimeout(() => setDebounced(query), 250);
    return () => clearTimeout(id);
  }, [query]);

  const { data: rows = [], isLoading, isError } = useQuery(clawhubSkillsOptions(debounced));
  const selected = search.dslug ? rows.find((s) => s.slug === search.dslug) : undefined;

  function selectSlug(slug?: string) {
    void navigate({
      to: projectId ? "/agents/$agentId/projects/$projectId/skills" : "/agents/$agentId/skills",
      params: projectId ? { agentId, projectId } : { agentId },
      search: slug ? { tab: "discover", dslug: slug } : { tab: "discover" },
      replace: true,
    });
  }

  async function install(skill: Pick<ClawhubSkill, "slug" | "name">) {
    setInstallingSlug(skill.slug);
    try {
      await installAgentSkill({
        path: { id: agentId },
        body: { source: `clawhub:${skill.slug}`, scope },
        throwOnError: true,
      });
      showToast(t("sessions.discover.installSuccess"), "success");
      void queryClient.invalidateQueries({ queryKey: ["agent-skills", agentId] });
    } catch (error) {
      showToast(apiErrorMessage(error, t("common.error")), "error");
    } finally {
      setInstallingSlug(null);
    }
  }

  const detail = search.dslug ? (
    <DiscoverDetail
      slug={search.dslug}
      row={selected}
      installedNames={installedNames}
      installedSources={installedSources}
      installingSlug={installingSlug}
      scope={scope}
      onScope={setScope}
      showAgentScope={!!me?.is_admin}
      onInstall={(slug) => void install({ slug, name: selected?.name ?? slug })}
      onClose={() => selectSlug()}
    />
  ) : null;

  return (
    <div className="flex h-full min-h-0">
      <div className="flex min-h-0 min-w-0 flex-1 flex-col">
        <div className="flex items-center gap-3 border-b p-3 sm:px-4">
          <InputGroup className="max-w-sm">
            <InputGroupAddon>
              <Search />
            </InputGroupAddon>
            <InputGroupInput
              nativeInput
              type="search"
              value={query}
              onChange={(e) => setQuery((e.target as HTMLInputElement).value)}
              placeholder={t("sessions.discover.searchPlaceholder")}
            />
          </InputGroup>
          {!isLoading && !isError && rows.length > 0 && (
            <span className="hidden text-xs text-muted-foreground sm:block">
              {t("sessions.discover.count", { n: rows.length })}
            </span>
          )}
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto p-3 sm:p-4">
          {isLoading ? (
            <div className="grid grid-cols-[repeat(auto-fill,minmax(260px,1fr))] gap-3">
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
          ) : isError ? (
            <Empty>
              <EmptyHeader>
                <EmptyMedia variant="icon">
                  <Blocks />
                </EmptyMedia>
                <EmptyTitle>{t("sessions.discover.emptyTitle")}</EmptyTitle>
                <EmptyDescription>{t("sessions.discover.loadError")}</EmptyDescription>
              </EmptyHeader>
            </Empty>
          ) : rows.length === 0 ? (
            <Empty>
              <EmptyHeader>
                <EmptyMedia variant="icon">
                  <Blocks />
                </EmptyMedia>
                <EmptyTitle>{t("sessions.discover.emptyTitle")}</EmptyTitle>
                <EmptyDescription>
                  {debounced.trim()
                    ? t("sessions.discover.noResults")
                    : t("sessions.discover.empty")}
                </EmptyDescription>
              </EmptyHeader>
            </Empty>
          ) : (
            <div className="grid grid-cols-[repeat(auto-fill,minmax(260px,1fr))] gap-3">
              {rows.map((skill) => (
                <DiscoverCard
                  key={skill.slug}
                  skill={skill}
                  active={skill.slug === search.dslug}
                  installed={isSkillInstalled(skill, installedNames, installedSources)}
                  installing={installingSlug === skill.slug}
                  installDisabled={installingSlug !== null}
                  onOpen={() => selectSlug(skill.slug)}
                  onInstall={() => void install(skill)}
                />
              ))}
            </div>
          )}
        </div>
      </div>
      {/* Desktop: full-height detail pane; mobile: Sheet */}
      {detail && !isMobile && (
        <div className="hidden min-h-0 w-[420px] shrink-0 flex-col border-l bg-card md:flex">
          {detail}
        </div>
      )}
      {isMobile && (
        <Sheet open={!!search.dslug} onOpenChange={(open) => !open && selectSlug()}>
          <SheetPopup side="right">
            <SheetPanel className="p-0">{detail}</SheetPanel>
          </SheetPopup>
        </Sheet>
      )}
      <ToastContainer messages={toasts} />
    </div>
  );
}

function DiscoverCard({
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
  scope: Scope;
  onScope: (scope: Scope) => void;
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
          <Button size="icon-sm" variant="ghost" onClick={onClose}>
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
      <div className="flex items-center gap-3 border-t p-4">
        {installed ? (
          <Badge variant="success">
            <Check />
            {t("sessions.discover.installed")}
          </Badge>
        ) : (
          <>
            <span className="text-xs text-muted-foreground">
              {t("sessions.discover.installTo")}
            </span>
            <ToggleGroup
              variant="outline"
              value={[scope]}
              onValueChange={(value: string[]) => value[0] && onScope(value[0] as Scope)}
            >
              <ToggleGroupItem value="user">
                {t("sessions.skillsList.profileScope")}
              </ToggleGroupItem>
              {showAgentScope && (
                <ToggleGroupItem value="agent">
                  {t("sessions.skillsList.agentScope")}
                </ToggleGroupItem>
              )}
            </ToggleGroup>
          </>
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
  );
}
