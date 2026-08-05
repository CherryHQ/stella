import { Fragment, useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import { useInfiniteQuery, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useRouterState } from "@tanstack/react-router";
import {
  Bot,
  ChevronDown,
  ChevronRight,
  Folder,
  FolderPlus,
  List,
  ListTodo,
  MessageSquare,
  MoreHorizontal,
  Pencil,
  Plus,
  SquarePen,
  Trash2,
  UserRound,
  Users,
} from "lucide-react";
import type { Agent, Project, Session } from "@/lib/types";
import type { ComponentsSession } from "@/lib/api-client/types.gen";
import {
  createProject,
  createSession as sdkCreateSession,
  deleteProject as sdkDeleteProject,
  deleteSession as sdkDeleteSession,
  getSessionWorkspace,
  updateSession as sdkUpdateSession,
} from "@/lib/api-client/sdk.gen";
import { useI18n } from "@/lib/i18n";
import { getAgentAvatarStyle } from "@/lib/agent-colors";
import { relativeTime } from "@/lib/relative-time";
import { cn } from "@/lib/utils";
import { agentsQueryOptions } from "@/lib/queries/agents";
import { goalCountsOptions } from "@/lib/queries/goals";
import {
  agentLevelChats,
  mainSessionQueryOptions,
  projectSessionsQueryOptions,
  sessionsInfiniteQueryOptions,
  sortedChats,
} from "@/lib/queries/sessions";
import { agentProjectsOptions } from "@/lib/queries/projects";
import { groupsQueryOptions, groupMembersQueryOptions } from "@/lib/queries/groups";
import { inboxQueryOptions } from "@/lib/queries/inbox";
import { SidebarItem, SidebarSection } from "@/components/AppSidebar";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogPanel,
  DialogPopup,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/menu";
import { useSidebar } from "@/components/ui/sidebar";
import { CreateGroupDialog } from "@/features/groups/CreateGroupDialog";

/** How many recent threads a page of "Recent threads" reveals. */
const RECENT_THREAD_PAGE = 5;
/** How many threads are inlined under the project the URL points at. */
const PROJECT_THREAD_LIMIT = 5;

/**
 * An icon action inside a sidebar row. SidebarItem already renders the row as a
 * link, so a nested <button> would be invalid markup — hence a span carrying the
 * button role and its keyboard handling. Hidden until the row is hovered or the
 * action itself is focused, so keyboard users can still reach it.
 */
function RowAction({
  label,
  group,
  onSelect,
  children,
}: {
  label: string;
  /** Tailwind group name of the owning row, e.g. `group-hover/project`. */
  group: string;
  onSelect: () => void;
  children: ReactNode;
}) {
  const activate = (event: { preventDefault: () => void; stopPropagation: () => void }) => {
    event.preventDefault();
    event.stopPropagation();
    onSelect();
  };
  return (
    <span
      role="button"
      tabIndex={0}
      aria-label={label}
      title={label}
      className={cn(
        "grid size-6 place-items-center rounded-lg text-muted-foreground opacity-0 transition-colors hover:bg-card hover:text-foreground focus-visible:opacity-100",
        group,
      )}
      onClick={activate}
      onKeyDown={(event) => {
        if (event.key === "Enter" || event.key === " ") activate(event);
      }}
    >
      {children}
    </span>
  );
}

interface DirEntry {
  path: string;
  name: string;
  depth: number;
}

function parseDirs(paths: string[]): DirEntry[] {
  return paths
    .filter((path) => path.endsWith("/"))
    .map((path) => {
      const clean = path.replace(/\/$/, "");
      const parts = clean.split("/");
      return { path: clean, name: parts[parts.length - 1], depth: parts.length - 1 };
    });
}

// Derive a filesystem-safe folder segment from a project name. Keeps Unicode
// (Chinese names are valid dir names) but neutralizes path separators and
// whitespace so the derived path stays a single new child of the root.
function toFolderSegment(name: string): string {
  return name
    .trim()
    .replace(/[/\\]+/g, "-")
    .replace(/\s+/g, "-")
    .replace(/^\.+/, "");
}

function joinPath(root: string, rel: string): string {
  const base = root.replace(/\/$/, "");
  return rel ? `${base}/${rel}` : base;
}

function FolderTree({
  agentId,
  sessionId,
  selected,
  onSelect,
  onRootResolved,
}: {
  agentId: string;
  sessionId: string;
  selected: string;
  onSelect: (path: string) => void;
  onRootResolved: (root: string) => void;
}) {
  const { t } = useI18n();
  const [dirs, setDirs] = useState<DirEntry[]>([]);
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    getSessionWorkspace({
      path: { agentId, sessionId },
      query: { depth: 4 },
      throwOnError: true,
    }).then(
      ({ data }) => {
        if (cancelled) return;
        setDirs(parseDirs(data.paths));
        if (data.root) onRootResolved(data.root);
        setLoading(false);
      },
      () => {
        if (!cancelled) setLoading(false);
      },
    );
    return () => {
      cancelled = true;
    };
  }, [agentId, sessionId, onRootResolved]);

  if (loading) {
    return <p className="px-2 py-3 text-xs text-muted-foreground">{t("common.loading")}</p>;
  }
  if (dirs.length === 0) {
    return (
      <p className="px-2 py-3 text-xs text-muted-foreground">{t("sessions.sidebar.noFolders")}</p>
    );
  }

  return (
    <div className="max-h-48 overflow-y-auto">
      {dirs
        .filter((dir) => {
          if (dir.depth === 0) return true;
          const parent = dir.path.split("/").slice(0, -1).join("/");
          return expanded.has(parent);
        })
        .map((dir) => {
          const hasChildren = dirs.some(
            (child) => child.depth === dir.depth + 1 && child.path.startsWith(`${dir.path}/`),
          );
          return (
            <button
              key={dir.path}
              type="button"
              className={cn(
                "flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-xs transition-colors",
                selected === dir.path
                  ? "bg-muted text-foreground"
                  : "text-muted-foreground hover:bg-muted/50 hover:text-foreground",
              )}
              style={{ paddingLeft: `${dir.depth * 16 + 8}px` }}
              onClick={() => onSelect(dir.path)}
            >
              <Folder className="size-3" />
              <span className="truncate">{dir.name}</span>
              {hasChildren && (
                <span
                  className="ml-auto flex size-4 items-center justify-center rounded text-muted-foreground hover:bg-muted"
                  onClick={(event) => {
                    event.stopPropagation();
                    setExpanded((prev) => {
                      const next = new Set(prev);
                      if (next.has(dir.path)) next.delete(dir.path);
                      else next.add(dir.path);
                      return next;
                    });
                  }}
                >
                  {expanded.has(dir.path) ? (
                    <ChevronDown className="size-3" />
                  ) : (
                    <ChevronRight className="size-3" />
                  )}
                </span>
              )}
            </button>
          );
        })}
    </div>
  );
}

function CreateProjectDialog({
  agentId,
  sessionId,
  onCreated,
  onClose,
}: {
  agentId: string;
  sessionId: string;
  onCreated: () => void;
  onClose: () => void;
}) {
  const { t } = useI18n();
  const [root, setRoot] = useState("");
  const [selectedDir, setSelectedDir] = useState("");
  const [name, setName] = useState("");
  const [showLocation, setShowLocation] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");

  // Two location modes share one form:
  //   auto   — selectedDir === "": the project gets a fresh folder derived
  //            from its name (<root>/<segment>); the common case, no browsing.
  //   custom — selectedDir set: attach to an existing folder the user picked.
  const custom = !!selectedDir;
  const segment = toFolderSegment(name);
  const relDir = custom ? selectedDir : segment;
  const folderBasename = selectedDir.split("/").pop() ?? "";
  const effectiveName = custom ? name.trim() || folderBasename : name.trim();
  const canSubmit = !!root && !!relDir && !!effectiveName && !submitting;

  const submit = useCallback(async () => {
    if (!canSubmit) return;
    setSubmitting(true);
    setError("");
    try {
      await createProject({
        path: { agentId },
        body: { name: effectiveName, base_dir: joinPath(root, relDir) },
        throwOnError: true,
      });
      onCreated();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to create project");
      setSubmitting(false);
    }
  }, [agentId, canSubmit, effectiveName, onCreated, relDir, root]);

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogPopup showCloseButton={false}>
        <DialogHeader>
          <DialogTitle>{t("sessions.sidebar.newProject")}</DialogTitle>
          <DialogDescription>{t("sessions.sidebar.projectNameDesc")}</DialogDescription>
        </DialogHeader>
        <DialogPanel className="space-y-5">
          {/* Name + its live location caption form one group. */}
          <div className="space-y-2.5">
            <Input
              autoFocus
              placeholder={t("sessions.sidebar.projectName")}
              value={name}
              onChange={(event) => setName(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === "Enter") void submit();
              }}
            />
            <div className="flex items-center gap-2 px-1 text-xs text-muted-foreground">
              <Folder className="size-3.5 shrink-0" />
              <span className="min-w-0 flex-1 truncate">
                {t("sessions.sidebar.workspaceRoot")}
                {relDir ? ` / ${relDir}` : ` / ${t("sessions.sidebar.projectName")}`}
              </span>
            </div>
          </div>

          {sessionId ? (
            <Collapsible open={showLocation} onOpenChange={setShowLocation} className="space-y-2.5">
              <div className="flex items-center justify-between">
                <CollapsibleTrigger render={<Button variant="ghost" size="xs" />}>
                  {showLocation ? <ChevronDown /> : <ChevronRight />}
                  {t("sessions.sidebar.changeLocation")}
                </CollapsibleTrigger>
                {custom && (
                  <Button variant="ghost" size="xs" onClick={() => setSelectedDir("")}>
                    {t("sessions.sidebar.useDefaultFolder")}
                  </Button>
                )}
              </div>
              <CollapsibleContent keepMounted>
                <div className="overflow-hidden rounded-lg border border-border p-1">
                  <FolderTree
                    agentId={agentId}
                    sessionId={sessionId}
                    selected={selectedDir}
                    onSelect={setSelectedDir}
                    onRootResolved={setRoot}
                  />
                </div>
              </CollapsibleContent>
            </Collapsible>
          ) : (
            <p className="text-xs text-muted-foreground">{t("sessions.sidebar.noActiveSession")}</p>
          )}

          {error && <p className="text-xs text-destructive">{error}</p>}
        </DialogPanel>
        <DialogFooter>
          <Button variant="ghost" size="sm" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button size="sm" disabled={!canSubmit} onClick={() => void submit()}>
            {submitting ? t("common.loading") : t("common.create")}
          </Button>
        </DialogFooter>
      </DialogPopup>
    </Dialog>
  );
}

interface TargetRow {
  kind: "agent" | "group";
  id: string;
  label: string;
  updatedAt: string;
  /** Position in the raw agents list — keeps avatar colors stable across sorts. */
  colorIndex: number;
}

/**
 * L1 navigation: one flat list of conversation targets (agents and groups),
 * with exactly one expanded — the one the URL points at. Expansion is derived,
 * never stored, so back/forward and deep links always agree with the sidebar.
 */
export function ConversationSidebar() {
  const { t } = useI18n();
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const { setOpenMobile } = useSidebar();
  const closeMobile = useCallback(() => setOpenMobile(false), [setOpenMobile]);
  const [showGroupDialog, setShowGroupDialog] = useState(false);

  const { data: agents = [] } = useQuery(agentsQueryOptions);
  const { data: groups = [] } = useQuery(groupsQueryOptions);
  const { data: inbox } = useQuery(inboxQueryOptions(undefined, 100));

  // Attention badges reuse the inbox feed rather than a second endpoint; items
  // without an agent belong to no row and are only counted by the top-bar bell.
  const attentionByAgent = useMemo(() => {
    const counts = new Map<string, number>();
    for (const item of inbox?.items ?? []) {
      if (!item.agent_id) continue;
      counts.set(item.agent_id, (counts.get(item.agent_id) ?? 0) + 1);
    }
    return counts;
  }, [inbox]);

  const activeAgentId = pathname.match(/\/agents\/([^/]+)/)?.[1] ?? "";
  const activeGroupId = pathname.match(/\/groups\/([^/]+)/)?.[1] ?? "";

  const targets = useMemo<TargetRow[]>(() => {
    const agentRows: TargetRow[] = agents.map((agent: Agent, index) => ({
      kind: "agent",
      id: agent.id,
      label: agent.name,
      updatedAt: agent.last_active ?? "",
      colorIndex: index,
    }));
    const groupRows: TargetRow[] = groups.map((group) => ({
      kind: "group",
      id: group.id,
      label: group.group_name || t("groups.unnamed"),
      updatedAt: group.last_active ?? group.updated_at,
      colorIndex: 0,
    }));
    return [...agentRows, ...groupRows].sort((a, b) => {
      if (!a.updatedAt && !b.updatedAt) return a.colorIndex - b.colorIndex;
      if (!a.updatedAt) return 1;
      if (!b.updatedAt) return -1;
      return new Date(b.updatedAt).getTime() - new Date(a.updatedAt).getTime();
    });
  }, [agents, groups, t]);

  return (
    <div className="flex min-h-0 w-full flex-col overflow-hidden">
      <div className="flex-1 overflow-y-auto overflow-x-hidden px-3 py-2">
        {/* The list gets the same muted anchor row as Projects and Recent
            threads, and creating a target is that row's action rather than a
            labelled button stranded at the bottom of the panel. */}
        <SidebarSection
          title={t("nav.agents")}
          className="mt-0"
          action={
            <DropdownMenu>
              <DropdownMenuTrigger
                render={
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    aria-label={t("sidebar.addTarget")}
                    title={t("sidebar.addTarget")}
                  />
                }
              >
                <Plus />
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" sideOffset={6}>
                <DropdownMenuItem render={<Link to="/settings/agents" />}>
                  <Bot className="size-4" />
                  {t("sidebar.newAgent")}
                </DropdownMenuItem>
                <DropdownMenuItem onClick={() => setShowGroupDialog(true)}>
                  <Users className="size-4" />
                  {t("sidebar.newGroup")}
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          }
        >
          {targets.map((target) => {
            const expanded =
              target.kind === "agent"
                ? !activeGroupId && activeAgentId === target.id
                : activeGroupId === target.id;
            if (target.kind === "agent") {
              return (
                <AgentNode
                  key={`agent:${target.id}`}
                  target={target}
                  expanded={expanded}
                  attention={attentionByAgent.get(target.id) ?? 0}
                  onNavigate={closeMobile}
                />
              );
            }
            return (
              <div key={`group:${target.id}`} className="min-w-0">
                <SidebarItem
                  active={expanded}
                  icon={<Users className="size-4" />}
                  label={target.label}
                  meta={
                    target.updatedAt ? (
                      <span className="font-mono text-xs">{relativeTime(target.updatedAt)}</span>
                    ) : undefined
                  }
                  to="/groups/$groupId"
                  params={{ groupId: target.id }}
                  onClick={closeMobile}
                />
                {expanded && <GroupBranch groupId={target.id} />}
              </div>
            );
          })}
          {targets.length === 0 && (
            <p className="px-2 py-2 text-xs text-muted-foreground">
              {t("sessions.sidebar.noAgents")}
            </p>
          )}
        </SidebarSection>
      </div>

      <CreateGroupDialog open={showGroupDialog} onClose={() => setShowGroupDialog(false)} />
    </div>
  );
}

/**
 * One agent row plus, when the URL points at it, its branch. The row is the
 * destination for the agent's main conversation — there is no separate "main"
 * child row — so it reads as active on the agent root and on the main session.
 */
function AgentNode({
  target,
  expanded,
  attention,
  onNavigate,
}: {
  target: TargetRow;
  expanded: boolean;
  attention: number;
  onNavigate: () => void;
}) {
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const { data: mainSession = null } = useQuery({
    ...mainSessionQueryOptions(target.id),
    enabled: expanded,
  });

  const activeSessionId = pathname.match(/\/sessions\/([^/]+)/)?.[1] ?? "";
  const active =
    expanded &&
    (pathname === `/agents/${target.id}` || (!!mainSession && activeSessionId === mainSession.id));

  return (
    <div className="min-w-0">
      <SidebarItem
        // An expanded agent that is not itself the destination only heads a
        // branch: emphasized, not filled, so exactly one row reads "you are here".
        active={active}
        emphasized={expanded && !active}
        icon={
          <span
            className="grid size-6 place-items-center rounded-full text-xs font-semibold text-primary-foreground"
            style={getAgentAvatarStyle(target.id, target.colorIndex)}
          >
            {target.label[0]?.toUpperCase()}
          </span>
        }
        label={target.label}
        badge={
          attention > 0 ? (
            <span className="shrink-0 rounded-full bg-muted px-2 py-0.5 font-mono text-xs text-foreground">
              {attention}
            </span>
          ) : undefined
        }
        meta={
          target.updatedAt ? (
            <span className="font-mono text-xs">{relativeTime(target.updatedAt)}</span>
          ) : undefined
        }
        to="/agents/$agentId"
        params={{ agentId: target.id }}
        onClick={onNavigate}
      />
      {expanded && <AgentBranch agentId={target.id} onNavigate={onNavigate} />}
    </div>
  );
}

function AgentBranch({ agentId, onNavigate }: { agentId: string; onNavigate: () => void }) {
  const { t } = useI18n();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  // Section open/closed is a glance-level preference, not navigation: it must
  // not survive a refresh, so it stays local. `null` means "nobody touched it",
  // which lets the route keep the section holding the active row open.
  const [projectsOverride, setProjectsOverride] = useState<boolean | null>(null);
  const [threadsOverride, setThreadsOverride] = useState<boolean | null>(null);
  const [visibleThreads, setVisibleThreads] = useState(RECENT_THREAD_PAGE);
  const [showProjectDialog, setShowProjectDialog] = useState(false);
  const [editingSessionId, setEditingSessionId] = useState("");
  const [editingTitle, setEditingTitle] = useState("");
  const [renaming, setRenaming] = useState(false);

  const { data: mainSession = null } = useQuery(mainSessionQueryOptions(agentId));
  const { data: projects = [] } = useQuery(agentProjectsOptions(agentId));
  const { data: goalCounts } = useQuery(goalCountsOptions(agentId));
  const chatsQuery = useInfiniteQuery(sessionsInfiniteQueryOptions(agentId, "chat"));

  const activeSessionId = pathname.match(/\/sessions\/([^/]+)/)?.[1] ?? "";
  const activeProjectId = pathname.match(/\/projects\/([^/]+)/)?.[1] ?? "";
  const onGoals = pathname.includes(`/agents/${agentId}/goals`);
  const onProfile = pathname === `/agents/${agentId}/profile`;

  const projectSessions = useQuery(projectSessionsQueryOptions(agentId, activeProjectId));

  // "main" is the pinned conversation and scheduler/task/delegate sessions are
  // machine-owned; recent only ever lists what the user started by hand, and
  // only at agent level — a project thread's home is its project.
  const recentThreads = useMemo(
    () => agentLevelChats(chatsQuery.data?.pages.flatMap((page) => page.sessions) ?? []),
    [chatsQuery.data],
  );
  // Only the project the URL points at gets its threads inlined — every other
  // project stays a single row so the section keeps its flat shape.
  const activeProjectThreads = useMemo(
    () => sortedChats(projectSessions.data ?? []).slice(0, PROJECT_THREAD_LIMIT),
    [projectSessions.data],
  );

  // A section that holds the current route opens itself, so the active row is
  // never hidden behind a collapsed label. Derived from the URL rather than the
  // loaded page, so it holds even before the thread list has paged in.
  const projectsOpen = projectsOverride ?? !!activeProjectId;
  const onThread = !activeProjectId && !!activeSessionId && activeSessionId !== mainSession?.id;
  const threadsOpen = threadsOverride ?? onThread;

  const refreshSessions = useCallback(async () => {
    await queryClient.invalidateQueries({ queryKey: ["sessions", agentId] });
  }, [agentId, queryClient]);

  const createChat = useCallback(async () => {
    const { data } = await sdkCreateSession({
      path: { agentId },
      body: { kind: "chat" },
      throwOnError: true,
    });
    const session = data as ComponentsSession;
    await refreshSessions();
    onNavigate();
    void navigate({
      to: "/agents/$agentId/sessions/$sessionId",
      params: { agentId, sessionId: session.id },
    });
  }, [agentId, navigate, onNavigate, refreshSessions]);

  // A project thread is created explicitly from the project it belongs to —
  // never inferred from "where you happened to be" — and opens at its own home.
  const createProjectChat = useCallback(
    async (projectId: string) => {
      const { data } = await sdkCreateSession({
        path: { agentId },
        body: { kind: "chat", project_id: projectId },
        throwOnError: true,
      });
      const session = data as ComponentsSession;
      await refreshSessions();
      onNavigate();
      void navigate({
        to: "/agents/$agentId/projects/$projectId/sessions/$sessionId",
        params: { agentId, projectId, sessionId: session.id },
      });
    },
    [agentId, navigate, onNavigate, refreshSessions],
  );

  const deleteProject = useCallback(
    async (projectId: string) => {
      if (!window.confirm(t("sessions.sidebar.deleteProjectConfirm"))) return;
      await sdkDeleteProject({ path: { agentId, projectId }, throwOnError: true });
      await queryClient.invalidateQueries({ queryKey: ["projects", agentId] });
    },
    [agentId, queryClient, t],
  );

  const cancelRename = useCallback(() => {
    if (renaming) return;
    setEditingSessionId("");
    setEditingTitle("");
  }, [renaming]);

  const renameSession = useCallback(
    async (session: Session, currentTitle: string) => {
      if (renaming) return;
      const title = editingTitle.trim();
      if (!title || title === currentTitle) {
        cancelRename();
        return;
      }
      setRenaming(true);
      try {
        await sdkUpdateSession({
          path: { agentId, sessionId: session.id },
          body: { title },
          throwOnError: true,
        });
        await refreshSessions();
        setEditingSessionId("");
        setEditingTitle("");
      } catch {
        setEditingTitle(currentTitle);
      } finally {
        setRenaming(false);
      }
    },
    [agentId, cancelRename, editingTitle, refreshSessions, renaming],
  );

  const deleteChat = useCallback(
    async (session: Session) => {
      if (!window.confirm(t("sessions.sidebar.deleteThreadConfirm"))) return;
      await sdkDeleteSession({ path: { agentId, sessionId: session.id }, throwOnError: true });
      await refreshSessions();
      if (activeSessionId === session.id) {
        onNavigate();
        void navigate({ to: "/agents/$agentId", params: { agentId } });
      }
    },
    [activeSessionId, agentId, navigate, onNavigate, refreshSessions, t],
  );

  return (
    // No rail, no deep nesting: one shallow indent, and grouping is carried by
    // the muted section labels and the space around them.
    <div className="ml-2 grid min-w-0 gap-0.5 pb-2 pt-0.5">
      <SidebarItem
        icon={<SquarePen className="size-4" />}
        label={t("sessions.sidebar.newThread")}
        onClick={() => void createChat()}
      />
      <SidebarItem
        active={onGoals}
        icon={<ListTodo className="size-4" />}
        label={t("sidebar.goals")}
        badge={
          goalCounts && goalCounts.active > 0 ? (
            <span className="shrink-0 rounded-full bg-muted px-2 py-0.5 font-mono text-xs text-foreground">
              {goalCounts.active}
            </span>
          ) : undefined
        }
        to="/agents/$agentId/goals"
        params={{ agentId }}
        onClick={onNavigate}
      />
      <SidebarItem
        active={onProfile}
        icon={<UserRound className="size-4" />}
        label={t("profile.title")}
        to="/agents/$agentId/profile"
        params={{ agentId }}
        onClick={onNavigate}
      />

      <SidebarSection
        title={t("sidebar.projects")}
        open={projectsOpen}
        onOpenChange={setProjectsOverride}
        count={projects.length}
        action={
          <Button
            variant="ghost"
            size="icon-sm"
            title={t("sessions.sidebar.newProject2")}
            onClick={() => setShowProjectDialog(true)}
          >
            <FolderPlus />
          </Button>
        }
      >
        {(projects as Project[]).map((project) => (
          <Fragment key={project.id}>
            <SidebarItem
              active={activeProjectId === project.id}
              className="group/project"
              icon={<Folder className="size-4" />}
              label={project.name}
              trailing={
                <span className="flex shrink-0 items-center gap-0.5">
                  <RowAction
                    label={t("sessions.sidebar.newProjectThread")}
                    group="group-hover/project:opacity-70"
                    onSelect={() => void createProjectChat(project.id)}
                  >
                    <Plus className="size-4" />
                  </RowAction>
                  <RowAction
                    label={t("sessions.sidebar.deleteProject")}
                    group="group-hover/project:opacity-70"
                    onSelect={() => void deleteProject(project.id)}
                  >
                    <Trash2 className="size-4" />
                  </RowAction>
                </span>
              }
              to="/agents/$agentId/projects/$projectId"
              params={{ agentId, projectId: project.id }}
              onClick={onNavigate}
            />
            {activeProjectId === project.id && activeProjectThreads.length > 0 && (
              <div className="grid min-w-0 gap-0.5 pl-4">
                {activeProjectThreads.map((session) => (
                  <SidebarItem
                    key={session.id}
                    active={activeSessionId === session.id}
                    icon={<MessageSquare className="size-4" />}
                    label={session.title || t("sessions.untitled")}
                    meta={
                      <time className="font-mono text-xs">{relativeTime(session.last_active)}</time>
                    }
                    to="/agents/$agentId/projects/$projectId/sessions/$sessionId"
                    params={{ agentId, projectId: project.id, sessionId: session.id }}
                    onClick={onNavigate}
                  />
                ))}
              </div>
            )}
          </Fragment>
        ))}
      </SidebarSection>

      {/* An agent with no threads and no projects gets no label and no
          placeholder — an empty section is noise the user cannot act on. With
          projects around, the label stays so "view all" (the only route to
          project threads in bulk) is reachable. */}
      {(recentThreads.length > 0 || projects.length > 0) && (
        <SidebarSection
          title={t("sidebar.recentThreads")}
          open={threadsOpen}
          onOpenChange={setThreadsOverride}
          count={recentThreads.length}
          action={
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label={t("sidebar.viewAllThreads")}
              title={t("sidebar.viewAllThreads")}
              render={<Link to="/agents/$agentId/threads" params={{ agentId }} />}
              onClick={onNavigate}
            >
              <List />
            </Button>
          }
        >
          {recentThreads.slice(0, visibleThreads).map((session: Session) => {
            const label = session.title || t("sessions.untitled");
            return (
              <SidebarItem
                key={session.id}
                active={activeSessionId === session.id}
                className="group/chat"
                icon={<MessageSquare className="size-4" />}
                label={
                  editingSessionId === session.id ? (
                    <Input
                      autoFocus
                      unstyled
                      size="sm"
                      className="w-full min-w-0"
                      disabled={renaming}
                      value={editingTitle}
                      onBlur={() => void renameSession(session, label)}
                      onChange={(event) => setEditingTitle(event.target.value)}
                      onClick={(event) => event.stopPropagation()}
                      onFocus={(event) => event.currentTarget.select()}
                      onKeyDown={(event) => {
                        if (event.key === "Enter") {
                          event.preventDefault();
                          void renameSession(session, label);
                        }
                        if (event.key === "Escape") {
                          event.preventDefault();
                          cancelRename();
                        }
                      }}
                    />
                  ) : (
                    label
                  )
                }
                meta={
                  <time className="font-mono text-xs">{relativeTime(session.last_active)}</time>
                }
                trailing={
                  editingSessionId === session.id ? undefined : (
                    <DropdownMenu>
                      <DropdownMenuTrigger
                        // SidebarItem already renders the row as a link or a button,
                        // so a nested <button> would be invalid markup. Declaring the
                        // trigger non-native makes Base UI supply the button role and
                        // keyboard handling instead.
                        nativeButton={false}
                        render={
                          <span
                            className="grid size-6 place-items-center rounded-lg text-muted-foreground opacity-0 transition-colors hover:bg-card hover:text-foreground group-hover/chat:opacity-70"
                            onClick={(event) => {
                              event.preventDefault();
                              event.stopPropagation();
                            }}
                          />
                        }
                      >
                        <MoreHorizontal className="size-4" />
                      </DropdownMenuTrigger>
                      <DropdownMenuContent align="end">
                        <DropdownMenuItem
                          onMouseDown={(event) => {
                            event.preventDefault();
                            setEditingSessionId(session.id);
                            setEditingTitle(label);
                          }}
                        >
                          <Pencil className="size-4" />
                          {t("sessions.sidebar.renameThread")}
                        </DropdownMenuItem>
                        <DropdownMenuItem
                          onMouseDown={(event) => {
                            event.preventDefault();
                            void deleteChat(session);
                          }}
                        >
                          <Trash2 className="size-4" />
                          {t("common.delete")}
                        </DropdownMenuItem>
                      </DropdownMenuContent>
                    </DropdownMenu>
                  )
                }
                to="/agents/$agentId/sessions/$sessionId"
                params={{ agentId, sessionId: session.id }}
                onClick={onNavigate}
              />
            );
          })}
          {(recentThreads.length > visibleThreads || chatsQuery.hasNextPage) && (
            <SidebarItem
              label={t("sidebar.showMore")}
              onClick={() => {
                setVisibleThreads((count) => count + RECENT_THREAD_PAGE);
                // Local slice first, network only once the cache runs dry.
                if (
                  recentThreads.length <= visibleThreads + RECENT_THREAD_PAGE &&
                  chatsQuery.hasNextPage &&
                  !chatsQuery.isFetchingNextPage
                ) {
                  void chatsQuery.fetchNextPage();
                }
              }}
            />
          )}
        </SidebarSection>
      )}

      {showProjectDialog && (
        <CreateProjectDialog
          agentId={agentId}
          sessionId={mainSession?.id ?? ""}
          onCreated={() => {
            setShowProjectDialog(false);
            void queryClient.invalidateQueries({ queryKey: ["projects", agentId] });
          }}
          onClose={() => setShowProjectDialog(false)}
        />
      )}
    </div>
  );
}

function GroupBranch({ groupId }: { groupId: string }) {
  const { t } = useI18n();
  const { data: members = [] } = useQuery(groupMembersQueryOptions(groupId));

  return (
    <div className="ml-4 grid min-w-0 gap-0.5 border-l border-border/60 pb-1 pl-2 pt-0.5">
      <SidebarSection title={t("sidebar.members")}>
        {members.map((member) => (
          <SidebarItem
            key={member.agent_id}
            icon={<Bot className="size-4" />}
            label={member.agent_name || member.agent_id}
            to="/agents/$agentId"
            params={{ agentId: member.agent_id }}
          />
        ))}
        {members.length === 0 && (
          <p className="px-2 py-2 text-xs text-muted-foreground">{t("sidebar.noMembers")}</p>
        )}
      </SidebarSection>
    </div>
  );
}
