import { useEffect, useState } from "react";
import { useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Blocks, Check, ChevronRight, Clock, Download, FileText, Search } from "lucide-react";
import { useToast, ToastContainer } from "@/hooks/use-toast";
import { installAgentSkill } from "@/lib/api-client/sdk.gen";
import type { ClawhubSkill } from "@/lib/api-client/types.gen";
import { clawhubSkillDetailOptions, clawhubSkillsOptions } from "@/lib/queries/agents";
import { useI18n } from "@/lib/i18n";
import { formatTime } from "@/lib/time";
import { cn } from "@/lib/utils";
import { MarkdownPreview } from "@/components/MarkdownPreview";
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Collapsible, CollapsiblePanel, CollapsibleTrigger } from "@/components/ui/collapsible";
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty";
import { Input } from "@/components/ui/input";
import { Separator } from "@/components/ui/separator";
import {
  Sheet,
  SheetFooter,
  SheetHeader,
  SheetPanel,
  SheetPopup,
  SheetTitle,
} from "@/components/ui/sheet";
import { Skeleton } from "@/components/ui/skeleton";

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
      <Blocks className="size-4.5" />
    </div>
  );
}

function AuthorChip({ handle, image }: { handle: string; image?: string }) {
  return (
    <span className="inline-flex items-center gap-1.5">
      <Avatar className="size-4">
        {image && <AvatarImage src={image} alt="" />}
        <AvatarFallback className="text-[8px] uppercase">{handle.slice(0, 1)}</AvatarFallback>
      </Avatar>
      {handle}
    </span>
  );
}

export function SkillsDiscover({
  agentId,
  installedNames,
}: {
  agentId: string;
  installedNames: Set<string>;
}) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { toasts, showToast } = useToast();
  const { projectId } = useParams({ strict: false }) as { projectId?: string };
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as { dslug?: string };
  const [query, setQuery] = useState("");
  const [debounced, setDebounced] = useState("");
  const [installingSlug, setInstallingSlug] = useState<string | null>(null);

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

  async function install(skill: ClawhubSkill) {
    setInstallingSlug(skill.slug);
    try {
      await installAgentSkill({
        path: { id: agentId },
        body: { source: `clawhub:${skill.slug}`, scope: "user" },
        throwOnError: true,
      });
      showToast(t("sessions.discover.installSuccess"), "success");
      void queryClient.invalidateQueries({ queryKey: ["agent-skills", agentId] });
    } catch (error) {
      showToast(error instanceof Error ? error.message : t("common.error"), "error");
    } finally {
      setInstallingSlug(null);
    }
  }

  return (
    <div className="space-y-4">
      <div className="relative max-w-sm">
        <Search
          className="absolute left-3 top-1/2 -translate-y-1/2 text-muted-foreground"
          size={16}
        />
        <Input
          nativeInput
          type="search"
          value={query}
          onChange={(e) => setQuery((e.target as HTMLInputElement).value)}
          placeholder={t("sessions.discover.searchPlaceholder")}
          className="pl-9"
        />
      </div>
      {isLoading ? (
        <div className="space-y-1">
          {Array.from({ length: 6 }).map((_, i) => (
            <div key={i} className="flex items-center gap-3 px-3 py-3">
              <Skeleton className="size-9 rounded-lg" />
              <div className="flex-1 space-y-2">
                <Skeleton className="h-4 w-40" />
                <Skeleton className="h-3 w-full max-w-md" />
              </div>
              <Skeleton className="h-8 w-16 rounded-md" />
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
              {debounced.trim() ? t("sessions.discover.noResults") : t("sessions.discover.empty")}
            </EmptyDescription>
          </EmptyHeader>
        </Empty>
      ) : (
        <div className="space-y-1">
          {rows.map((skill) => {
            const installed = installedNames.has(skill.name);
            const count = skill.installs ?? skill.downloads;
            return (
              <button
                key={skill.slug}
                type="button"
                onClick={() => selectSlug(skill.slug)}
                className="group flex w-full items-start gap-3 rounded-xl border border-transparent px-3 py-3 text-left transition-colors hover:border-border hover:bg-muted/40"
              >
                <SkillGlyph />
                <div className="min-w-0 flex-1 space-y-1">
                  <div className="flex items-center gap-2">
                    <span className="truncate font-mono text-sm font-medium">{skill.name}</span>
                    {skill.version && (
                      <Badge variant="outline" size="sm">
                        v{skill.version}
                      </Badge>
                    )}
                  </div>
                  {skill.summary && (
                    <p className="line-clamp-2 text-xs text-muted-foreground">{skill.summary}</p>
                  )}
                  <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
                    {count != null && (
                      <span className="inline-flex items-center gap-1">
                        <Download className="size-3.5" />
                        {t("sessions.discover.installs", { n: formatInstalls(count) })}
                      </span>
                    )}
                    {skill.author_handle && (
                      <AuthorChip handle={skill.author_handle} image={skill.author_image} />
                    )}
                  </div>
                </div>
                <div className="flex shrink-0 items-center gap-2 self-center">
                  {installed ? (
                    <Badge variant="success" size="sm">
                      <Check size={16} />
                      {t("sessions.discover.installed")}
                    </Badge>
                  ) : (
                    <Button
                      size="sm"
                      variant="outline"
                      loading={installingSlug === skill.slug}
                      disabled={installingSlug !== null}
                      onClick={(e) => {
                        e.stopPropagation();
                        void install(skill);
                      }}
                    >
                      {t("common.install")}
                    </Button>
                  )}
                  <ChevronRight className="size-4 text-muted-foreground/50 transition-transform group-hover:translate-x-0.5" />
                </div>
              </button>
            );
          })}
        </div>
      )}
      <Sheet open={!!search.dslug} onOpenChange={(open) => !open && selectSlug()}>
        <SheetPopup side="right" className="sm:max-w-xl">
          {search.dslug && (
            <DiscoverDetail
              slug={search.dslug}
              row={selected}
              installedNames={installedNames}
              installingSlug={installingSlug}
              onInstall={(slug) => void install({ slug, name: selected?.name ?? slug })}
            />
          )}
        </SheetPopup>
      </Sheet>
      <ToastContainer messages={toasts} />
    </div>
  );
}

function DiscoverDetail({
  slug,
  row,
  installedNames,
  installingSlug,
  onInstall,
}: {
  slug: string;
  row?: ClawhubSkill;
  installedNames: Set<string>;
  installingSlug: string | null;
  onInstall: (slug: string) => void;
}) {
  const { t } = useI18n();
  const { data, isLoading, isError } = useQuery(clawhubSkillDetailOptions(slug));
  const name = data?.name ?? row?.name ?? slug;
  const version = data?.version ?? row?.version;
  const summary = data?.summary ?? row?.summary;
  const count = row?.installs ?? row?.downloads;
  const installed = installedNames.has(name);
  const readme = stripFrontmatter(data?.readme ?? "").trim();
  const files = data?.files ?? [];

  return (
    <>
      <SheetHeader>
        <div className="flex items-start gap-3">
          <SkillGlyph className="size-11" />
          <div className="min-w-0 flex-1">
            <SheetTitle className="truncate font-mono">{name}</SheetTitle>
            <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
              {version && (
                <Badge variant="outline" size="sm">
                  v{version}
                </Badge>
              )}
              {count != null && (
                <span className="inline-flex items-center gap-1">
                  <Download className="size-3.5" />
                  {t("sessions.discover.installs", { n: formatInstalls(count) })}
                </span>
              )}
              {row?.author_handle && (
                <AuthorChip handle={row.author_handle} image={row.author_image} />
              )}
              {row?.updated_at && (
                <span className="inline-flex items-center gap-1">
                  <Clock className="size-3.5" />
                  {t("sessions.discover.updated", { t: formatTime(row.updated_at) })}
                </span>
              )}
            </div>
          </div>
        </div>
        {summary && <p className="mt-3 text-sm text-muted-foreground">{summary}</p>}
      </SheetHeader>
      <SheetPanel className="space-y-4">
        {files.length > 0 && (
          <Collapsible>
            <CollapsibleTrigger className="group flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
              <ChevronRight className="size-3.5 transition-transform group-data-[panel-open]:rotate-90" />
              {t("sessions.discover.files")} · {files.length}
            </CollapsibleTrigger>
            <CollapsiblePanel>
              <div className="mt-2 flex flex-wrap gap-1.5">
                {files.map((file) => (
                  <span
                    key={file}
                    className="inline-flex items-center gap-1 rounded-md bg-muted px-2 py-1 font-mono text-xs text-muted-foreground"
                  >
                    <FileText className="size-3" />
                    {file}
                  </span>
                ))}
              </div>
            </CollapsiblePanel>
          </Collapsible>
        )}
        <Separator />
        <div className="space-y-2">
          <p className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
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
      </SheetPanel>
      <SheetFooter>
        {installed ? (
          <Button variant="outline" className="w-full" disabled>
            <Check size={16} />
            {t("sessions.discover.installed")}
          </Button>
        ) : (
          <Button
            className="w-full"
            loading={installingSlug === slug}
            disabled={installingSlug !== null}
            onClick={() => onInstall(slug)}
          >
            {t("common.install")}
          </Button>
        )}
      </SheetFooter>
    </>
  );
}
