import { useEffect, useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Blocks,
  Check,
  ChevronLeft,
  Clock,
  Download,
  ExternalLink,
  FileText,
  GitFork,
  PackagePlus,
  Search,
  Store,
  Upload,
  X,
} from "lucide-react";
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
import { Input } from "@/components/ui/input";
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group";
import { Label } from "@/components/ui/label";
import { Sheet, SheetPopup } from "@/components/ui/sheet";
import { Skeleton } from "@/components/ui/skeleton";
import { Tooltip, TooltipPopup, TooltipTrigger } from "@/components/ui/tooltip";
import { SkillGlyph } from "@/features/skills/SkillGlyph";
import type { SkillNotify } from "@/features/skills/SkillInspectorPanel";
import type { ClawhubSkill } from "@/lib/api-client/types.gen";
import { installAgentSkill, uploadAgentSkill } from "@/lib/api-client/sdk.gen";
import { apiErrorMessage } from "@/lib/api-error";
import { useI18n } from "@/lib/i18n";
import {
  agentSkillsOptions,
  clawhubSkillDetailOptions,
  clawhubSkillsOptions,
} from "@/lib/queries/agents";
import { meQueryOptions } from "@/lib/queries/me";
import { INSTALL_SCOPES, SCOPE_DESC_KEY, SCOPE_LABEL_KEY } from "@/lib/skill-scope";
import { formatTime } from "@/lib/time";
import { cn } from "@/lib/utils";

type InstallScope = (typeof INSTALL_SCOPES)[number];
type Mode = "market" | "manual";

const MODE_META = {
  market: { icon: Store, key: "sessions.skillsList.market" },
  manual: { icon: PackagePlus, key: "sessions.skillsList.manualTab" },
} as const;

// Accent pill for the selected filter, muted ghost otherwise — the same active
// treatment the global top bar uses for its app tabs.
const tabPillCls = (active: boolean, size: "sm" | "xs" = "sm") =>
  cn(
    "inline-flex shrink-0 cursor-pointer items-center gap-1.5 rounded-md font-medium transition-colors",
    size === "sm" ? "h-8 px-3 text-sm" : "h-7 px-2.5 text-xs",
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

/**
 * The single "add a skill to this agent" surface: a right-side sheet with a
 * marketplace mode (search, browse, per-card install, README detail) and a
 * manual mode (GitHub repo, ZIP upload). Both share one install-scope choice,
 * gated for admins exactly as the backend gates the scope itself.
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
  const [scope, setScope] = useState<InstallScope>("user_agent");
  const [installingSlug, setInstallingSlug] = useState<string | null>(null);
  const [detailSlug, setDetailSlug] = useState<string | null>(null);

  useEffect(() => {
    const id = setTimeout(() => setDebounced(query), 250);
    return () => clearTimeout(id);
  }, [query]);

  const marketActive = open && mode === "market";
  // The marketplace needs the complete installed set to mark existing entries.
  const installedLookup = useQuery({ ...agentSkillsOptions(agentId), enabled: marketActive });
  const market = useQuery({ ...clawhubSkillsOptions(debounced), enabled: marketActive });
  const skills = useMemo(() => installedLookup.data ?? [], [installedLookup.data]);
  const installedNames = useMemo(() => new Set(skills.map((s) => s.name)), [skills]);
  const installedSources = useMemo(
    () => new Set(skills.map((s) => s.source).filter((src): src is string => !!src)),
    [skills],
  );
  const rows = market.data ?? [];
  const detailRow = detailSlug ? rows.find((r) => r.slug === detailSlug) : undefined;

  function invalidateSkills() {
    void queryClient.invalidateQueries({ queryKey: ["agent-skills", agentId] });
  }

  async function install(skill: Pick<ClawhubSkill, "slug" | "name">) {
    setInstallingSlug(skill.slug);
    try {
      await installAgentSkill({
        path: { id: agentId },
        body: { source: `clawhub:${skill.slug}`, scope },
        throwOnError: true,
      });
      notify(t("sessions.discover.installSuccess"), "success");
      invalidateSkills();
    } catch (error) {
      notify(apiErrorMessage(error, t("common.error")), "error");
    } finally {
      setInstallingSlug(null);
    }
  }

  function close() {
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
        {detailSlug ? (
          <DiscoverDetail
            slug={detailSlug}
            row={detailRow}
            installedNames={installedNames}
            installedSources={installedSources}
            installingSlug={installingSlug}
            scope={scope}
            onScope={setScope}
            showAgentScope={!!me?.is_admin}
            onInstall={(slug) => void install({ slug, name: detailRow?.name ?? slug })}
            onBack={() => setDetailSlug(null)}
          />
        ) : (
          <div className="flex h-full min-h-0 flex-col">
            <div className="flex items-center gap-3 border-b p-5">
              <h2 className="min-w-0 flex-1 truncate text-base font-semibold">
                {t("profile.addSkill")}
              </h2>
              <Button size="icon-sm" variant="ghost" aria-label={t("common.close")} onClick={close}>
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
                <InputGroup>
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
            </div>

            <div className="min-h-0 flex-1 overflow-y-auto p-4">
              {mode === "market" ? (
                <MarketGrid
                  query={market}
                  rows={rows}
                  installedNames={installedNames}
                  installedSources={installedSources}
                  installingSlug={installingSlug}
                  onOpen={setDetailSlug}
                  onInstall={(s) => void install(s)}
                />
              ) : (
                <ManualInstallPanel
                  agentId={agentId}
                  scope={scope}
                  onScope={setScope}
                  showAgentScope={!!me?.is_admin}
                  notify={notify}
                  onInstalled={() => {
                    notify(t("sessions.discover.installSuccess"), "success");
                    invalidateSkills();
                    close();
                  }}
                />
              )}
            </div>
          </div>
        )}
      </SheetPopup>
    </Sheet>
  );
}

function MarketGrid({
  query,
  rows,
  installedNames,
  installedSources,
  installingSlug,
  onOpen,
  onInstall,
}: {
  query: { isLoading: boolean; isError: boolean };
  rows: ClawhubSkill[];
  installedNames: Set<string>;
  installedSources: Set<string>;
  installingSlug: string | null;
  onOpen: (slug: string) => void;
  onInstall: (skill: Pick<ClawhubSkill, "slug" | "name">) => void;
}) {
  const { t } = useI18n();
  if (query.isLoading) {
    return (
      <div className="grid grid-cols-1 gap-3">
        {Array.from({ length: 4 }).map((_, i) => (
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
    <div className="grid grid-cols-1 gap-3">
      {rows.map((skill) => (
        <MarketCard
          key={skill.slug}
          skill={skill}
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
  installed,
  installing,
  installDisabled,
  onOpen,
  onInstall,
}: {
  skill: ClawhubSkill;
  installed: boolean;
  installing: boolean;
  installDisabled: boolean;
  onOpen: () => void;
  onInstall: () => void;
}) {
  const { t } = useI18n();
  const count = skill.installs ?? skill.downloads;
  return (
    // The card body and the install control are siblings, never nested: only the
    // upper block opens the detail view, the footer owns its own actions.
    <div className="flex flex-col gap-3 rounded-lg border border-border bg-card p-4">
      <button type="button" onClick={onOpen} className="flex flex-col gap-3 text-left">
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
          <p className="line-clamp-2 text-xs text-muted-foreground">{skill.summary}</p>
        )}
      </button>
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
        <span className="ml-auto">
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
  onBack,
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
  onBack: () => void;
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

// Manual install: point at a GitHub repo or upload a ZIP. Both methods share the
// sheet's install-scope choice, so a skill lands where the user picked once.
function ManualInstallPanel({
  agentId,
  scope,
  onScope,
  showAgentScope,
  notify,
  onInstalled,
}: {
  agentId: string;
  scope: InstallScope;
  onScope: (scope: InstallScope) => void;
  showAgentScope: boolean;
  notify: SkillNotify;
  onInstalled: () => void;
}) {
  const { t } = useI18n();

  function onError(error: unknown) {
    notify(apiErrorMessage(error, t("common.error")), "error");
  }

  return (
    <div className="space-y-5">
      <div className="space-y-1.5">
        <span className="text-xs text-muted-foreground">{t("sessions.discover.installTo")}</span>
        <InstallScopePicker
          scope={scope}
          onScope={onScope}
          showAgentScope={showAgentScope}
          size="sm"
        />
      </div>
      <GitHubInstallCard
        agentId={agentId}
        scope={scope}
        onInstalled={onInstalled}
        onError={onError}
      />
      <ZipUploadCard agentId={agentId} scope={scope} onInstalled={onInstalled} onError={onError} />
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
  onInstalled: () => void;
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
      await installAgentSkill({
        path: { id: agentId },
        body: { source: githubSource(repo, skill, version), scope },
        throwOnError: true,
      });
      setRepo("");
      setSkill("");
      setVersion("");
      onInstalled();
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
  onInstalled: () => void;
  onError: (error: unknown) => void;
}) {
  const { t } = useI18n();
  const [file, setFile] = useState<File | null>(null);
  const [busy, setBusy] = useState(false);

  async function upload() {
    if (!file) return;
    setBusy(true);
    try {
      await uploadAgentSkill({
        path: { id: agentId },
        body: { file, scope },
        throwOnError: true,
      });
      setFile(null);
      onInstalled();
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
        aria-label={t("sessions.skillsList.uploadZip")}
        onChange={(e) => setFile((e.target as HTMLInputElement).files?.[0] ?? null)}
      />
      <Button disabled={!file || busy} loading={busy} onClick={() => void upload()}>
        <Upload size={16} />
        {t("sessions.skillsList.uploadZip")}
      </Button>
    </section>
  );
}
