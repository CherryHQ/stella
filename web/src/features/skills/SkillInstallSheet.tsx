import { useEffect, useMemo, useRef, useState } from "react";
import { useInfiniteQuery, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Check,
  ChevronLeft,
  Clock,
  Download,
  ExternalLink,
  FileText,
  GitFork,
  PackagePlus,
  Store,
  Upload,
  X,
} from "lucide-react";
import { MarkdownPreview } from "@/components/MarkdownPreview";
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Sheet, SheetPopup } from "@/components/ui/sheet";
import { Skeleton } from "@/components/ui/skeleton";
import {
  InstallScopeStep,
  type InstallRequest,
  type InstallScope,
} from "@/features/marketplace/InstallScopeStep";
import { MarketCard } from "@/features/marketplace/MarketCard";
import { MarketGrid } from "@/features/marketplace/MarketGrid";
import { MarketSearch } from "@/features/marketplace/MarketSearch";
import { SkillGlyph } from "@/features/skills/SkillGlyph";
import type { SkillNotify } from "@/features/skills/SkillInspectorPanel";
import type { ClawhubSkill } from "@/lib/api-client/types.gen";
import { installAgentSkill, uploadAgentSkill } from "@/lib/api-client/sdk.gen";
import { apiErrorMessage } from "@/lib/api-error";
import { useI18n } from "@/lib/i18n";
import {
  agentSkillsOptions,
  clawhubSkillDetailOptions,
  clawhubSkillsInfiniteQueryOptions,
} from "@/lib/queries/agents";
import { meQueryOptions } from "@/lib/queries/me";

import { formatTime } from "@/lib/time";
import { targetValue, cn } from "@/lib/utils";

type Mode = "market" | "manual";

const MODE_META = {
  market: { icon: Store, key: "sessions.skillsList.market" },
  manual: { icon: PackagePlus, key: "sessions.skillsList.manualTab" },
} as const;

// Accent pill for the selected filter, muted ghost otherwise — the same active
// treatment the global top bar uses for its app tabs.
const tabPillCls = (active: boolean) =>
  cn(
    "inline-flex h-8 shrink-0 cursor-pointer items-center gap-1.5 rounded-md px-3 text-sm font-medium transition-colors",
    active
      ? "bg-accent text-accent-foreground"
      : "text-muted-foreground hover:bg-muted/50 hover:text-foreground",
  );

function formatInstalls(n: number): string {
  return n >= 1000 ? `${(n / 1000).toFixed(1).replace(/\.0$/, "")}k` : String(n);
}

// SKILL.md leads with a YAML frontmatter block; left in place markdown renders it as a
// giant setext heading, so drop it before previewing the human-readable body.
function stripFrontmatter(md: string): string {
  const match = md.match(/^\s*---\r?\n[\s\S]*?\r?\n---\r?\n?/);
  return match ? md.slice(match[0].length) : md;
}

function isSkillInstalled(
  skill: Pick<ClawhubSkill, "slug">,
  installedSources: Set<string>,
): boolean {
  return installedSources.has(`clawhub:${skill.slug}`);
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

type SkillErrorHandler = <T>(error: T) => void;

/**
 * The single "add a skill to this agent" surface: a right-side sheet with a
 * marketplace mode (search, browse, per-card install, README detail) and a
 * manual mode (GitHub repo, ZIP upload). Every install path funnels through
 * InstallScopeStep, so no write happens without a just-confirmed destination;
 * the scope is gated for admins exactly as the backend gates it.
 *
 * Market mode stays open after an install so more skills can be added in one
 * pass; a manual install closes the sheet because it installs exactly one.
 */
export function SkillInstallSheet({
  agentId,
  open,
  onOpenChange,
  notify,
}: {
  agentId: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  notify: SkillNotify;
}) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { data: me } = useQuery(meQueryOptions);
  const [mode, setMode] = useState<Mode>("market");
  const [query, setQuery] = useState("");
  const [debounced, setDebounced] = useState("");
  // Remembered default only: the last destination the user confirmed in this
  // session seeds the next dialog, but never installs anything on its own.
  const [scope, setScope] = useState<InstallScope>("user_agent");
  const [pending, setPending] = useState<InstallRequest | null>(null);
  const [installingSlug, setInstallingSlug] = useState<string | null>(null);
  const [detailSlug, setDetailSlug] = useState<string | null>(null);
  const contentRef = useRef<HTMLDivElement>(null);
  const sentinelRef = useRef<HTMLDivElement>(null);

  const marketActive = open && mode === "market";
  // The marketplace needs the complete installed set to mark existing entries.
  const installedLookup = useQuery({ ...agentSkillsOptions(agentId), enabled: marketActive });
  const market = useInfiniteQuery({
    ...clawhubSkillsInfiniteQueryOptions(debounced),
    enabled: marketActive,
  });
  const skills = useMemo(() => installedLookup.data ?? [], [installedLookup.data]);
  const installedSources = useMemo(
    () => new Set(skills.map((s) => s.source).filter((src): src is string => !!src)),
    [skills],
  );
  const rows = useMemo(
    () => (market.data?.pages ?? []).flatMap((page) => page.skills ?? []),
    [market.data?.pages],
  );
  const detailRow = detailSlug ? rows.find((r) => r.slug === detailSlug) : undefined;

  // Auto-load the next marketplace page as the sentinel nears the sheet's scroll
  // container. The rAF follow-up covers the case where an appended page still
  // leaves the list shorter than the viewport, which never re-triggers the observer.
  const listVisible = marketActive && !detailSlug;
  useEffect(() => {
    if (!listVisible || !market.hasNextPage || market.isFetchingNextPage) return;
    if (market.isFetchNextPageError) return;
    const root = contentRef.current;
    const sentinel = sentinelRef.current;
    if (!root || !sentinel) return;

    let requested = false;
    const loadNext = () => {
      if (requested) return;
      requested = true;
      void market.fetchNextPage();
    };
    const observer = new IntersectionObserver(
      ([entry]) => {
        if (entry?.isIntersecting) loadNext();
      },
      { root, rootMargin: "240px 0px" },
    );
    observer.observe(sentinel);

    const frame = requestAnimationFrame(() => {
      if (root.scrollHeight <= root.clientHeight + 1) loadNext();
    });
    return () => {
      cancelAnimationFrame(frame);
      observer.disconnect();
    };
  }, [
    listVisible,
    rows.length,
    market.hasNextPage,
    market.isFetchingNextPage,
    market.isFetchNextPageError,
    market.fetchNextPage,
  ]);

  function invalidateSkills() {
    void queryClient.invalidateQueries({ queryKey: ["agent-skills", agentId] });
  }

  async function install(
    skill: Pick<ClawhubSkill, "slug" | "name">,
    target: InstallScope,
  ): Promise<boolean> {
    setInstallingSlug(skill.slug);
    try {
      await installAgentSkill({
        path: { id: agentId },
        body: { source: `clawhub:${skill.slug}`, scope: target },
        throwOnError: true,
      });
      notify(t("sessions.discover.installSuccess"), "success");
      invalidateSkills();
      return true;
    } catch (error) {
      notify(apiErrorMessage(error, t("common.error")), "error");
      return false;
    } finally {
      setInstallingSlug(null);
    }
  }

  function requestMarketInstall(skill: Pick<ClawhubSkill, "slug" | "name">) {
    setPending({
      name: skill.name,
      confirmLabel: t("common.install"),
      run: (target) => install(skill, target),
    });
  }

  function close() {
    setPending(null);
    setDetailSlug(null);
    onOpenChange(false);
  }

  return (
    <Sheet open={open} onOpenChange={(next) => (next ? onOpenChange(true) : close())}>
      <SheetPopup
        side="right"
        showCloseButton={false}
        className="w-full sm:w-[560px] sm:max-w-[560px]"
      >
        <div className="relative flex h-full min-h-0 flex-col">
          {detailSlug ? (
            <DiscoverDetail
              slug={detailSlug}
              row={detailRow}
              installedSources={installedSources}
              installingSlug={installingSlug}
              onInstall={(slug) => requestMarketInstall({ slug, name: detailRow?.name ?? slug })}
              onBack={() => setDetailSlug(null)}
            />
          ) : (
            <>
              <div className="flex items-center gap-3 border-b p-5">
                <h2 className="min-w-0 flex-1 truncate text-base font-semibold">
                  {t("profile.addSkill")}
                </h2>
                <Button
                  size="icon-sm"
                  variant="ghost"
                  aria-label={t("common.close")}
                  onClick={close}
                >
                  <X size={16} />
                </Button>
              </div>

              <div className="flex flex-col gap-2.5 border-b p-4">
                <div className="flex flex-wrap items-center gap-1">
                  {(["market", "manual"] as const).map((m) => {
                    const Icon = MODE_META[m].icon;
                    return (
                      <button
                        key={m}
                        type="button"
                        aria-pressed={mode === m}
                        onClick={() => setMode(m)}
                        className={tabPillCls(mode === m)}
                      >
                        <Icon className="size-4" />
                        {t(MODE_META[m].key)}
                      </button>
                    );
                  })}
                </div>
                {mode === "market" && (
                  <MarketSearch
                    value={query}
                    onValueChange={setQuery}
                    onDebounce={setDebounced}
                    placeholder={t("sessions.skillsList.searchPlaceholder")}
                  />
                )}
              </div>

              <div ref={contentRef} className="min-h-0 flex-1 overflow-y-auto p-4">
                {mode === "market" ? (
                  <MarketGrid
                    isLoading={market.isLoading}
                    isError={market.isError}
                    isFetchingNextPage={market.isFetchingNextPage}
                    isFetchNextPageError={market.isFetchNextPageError}
                    hasNextPage={market.hasNextPage}
                    rows={rows}
                    sentinelRef={sentinelRef}
                    renderItem={(skill) => (
                      <MarketCard
                        key={skill.slug}
                        title={skill.name}
                        version={skill.version || null}
                        description={skill.summary || null}
                        authorChip={
                          skill.author_handle ? (
                            <AuthorChip handle={skill.author_handle} image={skill.author_image} />
                          ) : null
                        }
                        footerMeta={
                          (skill.installs ?? skill.downloads) != null ? (
                            <span className="inline-flex items-center gap-1">
                              <Download className="size-4" />
                              {formatInstalls(skill.installs ?? skill.downloads ?? 0)}
                            </span>
                          ) : null
                        }
                        installed={isSkillInstalled(skill, installedSources)}
                        installing={installingSlug === skill.slug}
                        installDisabled={installingSlug !== null}
                        onOpen={() => setDetailSlug(skill.slug)}
                        onInstall={() => requestMarketInstall(skill)}
                      />
                    )}
                    onRetry={() =>
                      void (market.isFetchNextPageError ? market.fetchNextPage() : market.refetch())
                    }
                    emptyTitleKey="sessions.discover.emptyTitle"
                    emptyDescriptionKey="sessions.discover.empty"
                  />
                ) : (
                  <ManualInstallPanel
                    agentId={agentId}
                    requestInstall={setPending}
                    notify={notify}
                    onInstalled={() => {
                      notify(t("sessions.discover.installSuccess"), "success");
                      invalidateSkills();
                      close();
                    }}
                  />
                )}
              </div>
            </>
          )}
          {pending && (
            <InstallScopeStep
              request={pending}
              defaultScope={scope}
              showAgentScope={!!me?.is_admin}
              onConfirmed={(target) => {
                setScope(target);
                setPending(null);
              }}
              onCancel={() => setPending(null)}
            />
          )}
        </div>
      </SheetPopup>
    </Sheet>
  );
}

function DiscoverDetail({
  slug,
  row,
  installedSources,
  installingSlug,
  onInstall,
  onBack,
}: {
  slug: string;
  row?: ClawhubSkill;
  installedSources: Set<string>;
  installingSlug: string | null;
  onInstall: (slug: string) => void;
  onBack: () => void;
}) {
  const { t } = useI18n();
  const { data, isLoading, isError } = useQuery(clawhubSkillDetailOptions(slug));
  const name = data?.name ?? row?.name ?? slug;
  const version = data?.version ?? row?.version;
  const summary = data?.summary ?? row?.summary;
  const count = row?.installs ?? row?.downloads;
  const installed = isSkillInstalled({ slug }, installedSources);
  const readme = stripFrontmatter(data?.readme ?? "").trim();
  const files = data?.files ?? [];

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="border-b p-5">
        <div className="flex items-start gap-3">
          <Button size="icon-sm" variant="ghost" aria-label={t("common.back")} onClick={onBack}>
            <ChevronLeft size={16} />
          </Button>
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
      <div className="border-t p-4">
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

// Manual install: point at a GitHub repo or upload a ZIP. Each card validates
// its own inputs, then hands the write to the scope dialog rather than firing it.
function ManualInstallPanel({
  agentId,
  requestInstall,
  notify,
  onInstalled,
}: {
  agentId: string;
  requestInstall: (request: InstallRequest) => void;
  notify: SkillNotify;
  onInstalled: () => void;
}) {
  const { t } = useI18n();

  function onError<T>(error: T) {
    notify(apiErrorMessage(error, t("common.error")), "error");
  }

  return (
    <div className="space-y-5">
      <GitHubInstallCard
        agentId={agentId}
        requestInstall={requestInstall}
        onInstalled={onInstalled}
        onError={onError}
      />
      <ZipUploadCard
        agentId={agentId}
        requestInstall={requestInstall}
        onInstalled={onInstalled}
        onError={onError}
      />
    </div>
  );
}

function GitHubInstallCard({
  agentId,
  requestInstall,
  onInstalled,
  onError,
}: {
  agentId: string;
  requestInstall: (request: InstallRequest) => void;
  onInstalled: () => void;
  onError: SkillErrorHandler;
}) {
  const { t } = useI18n();
  const [repo, setRepo] = useState("");
  const [skill, setSkill] = useState("");
  const [version, setVersion] = useState("");
  const ready = repo.trim() !== "" && skill.trim() !== "";

  function askInstall() {
    if (!ready) return;
    const source = githubSource(repo, skill, version);
    requestInstall({
      name: skill.trim(),
      confirmLabel: t("common.install"),
      run: async (scope) => {
        try {
          await installAgentSkill({
            path: { id: agentId },
            body: { source, scope },
            throwOnError: true,
          });
          setRepo("");
          setSkill("");
          setVersion("");
          onInstalled();
          return true;
        } catch (error) {
          onError(error);
          return false;
        }
      },
    });
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
          onChange={(e) => setRepo(targetValue(e))}
          placeholder={t("sessions.skillsList.githubRepoPlaceholder")}
        />
      </div>
      <div className="space-y-1.5">
        <Label>{t("sessions.skillsList.githubSkill")}</Label>
        <Input
          nativeInput
          autoComplete="off"
          value={skill}
          onChange={(e) => setSkill(targetValue(e))}
          placeholder={t("sessions.skillsList.githubSkillPlaceholder")}
        />
      </div>
      <div className="space-y-1.5">
        <Label>{t("sessions.skillsList.githubVersion")}</Label>
        <Input
          nativeInput
          autoComplete="off"
          value={version}
          onChange={(e) => setVersion(targetValue(e))}
          placeholder={t("sessions.skillsList.githubVersionPlaceholder")}
        />
      </div>
      <p className="text-xs text-muted-foreground">{t("sessions.skillsList.githubHint")}</p>
      <Button disabled={!ready} onClick={askInstall}>
        <GitFork size={16} />
        {t("common.install")}
      </Button>
    </section>
  );
}

function ZipUploadCard({
  agentId,
  requestInstall,
  onInstalled,
  onError,
}: {
  agentId: string;
  requestInstall: (request: InstallRequest) => void;
  onInstalled: () => void;
  onError: SkillErrorHandler;
}) {
  const { t } = useI18n();
  const [file, setFile] = useState<File | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);

  function askUpload() {
    if (!file) return;
    const picked = file;
    requestInstall({
      name: picked.name,
      confirmLabel: t("sessions.skillsList.uploadZip"),
      run: async (scope) => {
        try {
          await uploadAgentSkill({
            path: { id: agentId },
            body: { file: picked, scope },
            throwOnError: true,
          });
          setFile(null);
          // The file input keeps its own value; clear it so the cleared state shows.
          if (inputRef.current) inputRef.current.value = "";
          onInstalled();
          return true;
        } catch (error) {
          onError(error);
          return false;
        }
      },
    });
  }

  const handleFileChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    // SAFETY: the file input's change target carries the picked FileList.
    setFile((e.target as HTMLInputElement).files?.[0] ?? null);
  };

  return (
    <section className="space-y-3 rounded-lg border p-4">
      <div className="flex items-center gap-2 text-sm font-medium">
        <Upload className="size-4" />
        {t("sessions.skillsList.uploadZip")}
      </div>
      <Input
        ref={inputRef}
        nativeInput
        type="file"
        accept=".zip"
        aria-label={t("sessions.skillsList.uploadZip")}
        onChange={handleFileChange}
      />
      <Button disabled={!file} onClick={askUpload}>
        <Upload size={16} />
        {t("sessions.skillsList.uploadZip")}
      </Button>
    </section>
  );
}
