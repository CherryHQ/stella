import { useCallback, useEffect, useMemo, useState } from "react";
import { useInfiniteQuery, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate, useRouterState } from "@tanstack/react-router";
import { Bell, Folder, FolderPlus, MessageSquarePlus, MoreHorizontal, Users } from "lucide-react";
import type { Agent, Project, Session } from "@/lib/types";
import type { ComponentsSession } from "@/lib/api-client/types.gen";
import {
  createProject,
  createSession as sdkCreateSession,
  deleteProject as sdkDeleteProject,
  getSessionWorkspace,
} from "@/lib/api-client/sdk.gen";
import { useI18n } from "@/lib/i18n";
import { getAgentColor } from "@/lib/agent-colors";
import { cn } from "@/lib/utils";
import { mainSessionQueryOptions, sessionsInfiniteQueryOptions } from "@/lib/queries/sessions";
import { agentProjectsOptions } from "@/lib/queries/projects";
import { groupsQueryOptions } from "@/lib/queries/groups";
import { inboxQueryOptions } from "@/lib/queries/inbox";
import { SidebarItem, SidebarSection } from "@/components/AppSidebar";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogPopup,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { useSidebar } from "@/components/ui/sidebar";
import { CreateGroupDialog } from "@/features/groups/CreateGroupDialog";

interface Props {
  agents: Agent[];
  agentId: string;
  onAgentChange: (id: string) => void;
}

interface DirEntry {
  path: string;
  name: string;
  depth: number;
}

function relativeTime(dateStr?: string): string {
  if (!dateStr) return "";
  const diff = Date.now() - new Date(dateStr).getTime();
  if (diff < 60000) return "now";
  const minutes = Math.floor(diff / 60000);
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h`;
  const days = Math.floor(hours / 24);
  if (days < 7) return `${days}d`;
  const weeks = Math.floor(days / 7);
  if (weeks < 5) return `${weeks}w`;
  return `${Math.floor(days / 30)}mo`;
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
                "flex w-full items-center gap-2 rounded px-2 py-1 text-left text-xs transition-colors",
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
                  className="ml-auto text-muted-foreground"
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
                  {expanded.has(dir.path) ? "-" : "+"}
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
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");
  const folderName = selectedDir.split("/").pop() ?? "";
  const effectiveName = name || folderName;
  const canSubmit = !!root && !!selectedDir && !!effectiveName && !submitting;

  const submit = useCallback(async () => {
    if (!canSubmit) return;
    setSubmitting(true);
    setError("");
    try {
      await createProject({
        path: { agentId },
        body: { name: effectiveName, base_dir: `${root}/${selectedDir}` },
        throwOnError: true,
      });
      onCreated();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to create project");
      setSubmitting(false);
    }
  }, [agentId, canSubmit, effectiveName, onCreated, root, selectedDir]);

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogPopup showCloseButton={false}>
        <DialogHeader>
          <DialogTitle>{t("sessions.sidebar.newProject")}</DialogTitle>
          <DialogDescription>{t("sessions.sidebar.selectFolder")}</DialogDescription>
        </DialogHeader>
        <div className="space-y-3 px-6 py-2">
          {sessionId ? (
            <div className="overflow-hidden rounded-lg border border-border">
              <FolderTree
                agentId={agentId}
                sessionId={sessionId}
                selected={selectedDir}
                onSelect={(path) => {
                  setSelectedDir(path);
                  setName("");
                }}
                onRootResolved={setRoot}
              />
            </div>
          ) : (
            <p className="text-xs text-muted-foreground">{t("sessions.sidebar.noActiveSession")}</p>
          )}
          <Input
            placeholder={folderName || t("sessions.sidebar.projectName")}
            value={name}
            onChange={(event) => setName(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === "Enter") void submit();
            }}
          />
          {error && <p className="text-xs text-destructive">{error}</p>}
        </div>
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

export function AgentAppSidebar({ agents, agentId, onAgentChange }: Props) {
  const { t } = useI18n();
  const navigate = useNavigate();
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const queryClient = useQueryClient();
  const { setOpenMobile } = useSidebar();
  const closeMobile = useCallback(() => setOpenMobile(false), [setOpenMobile]);
  const [chatsOpen, setChatsOpen] = useState(false);
  const [projectsOpen, setProjectsOpen] = useState(true);
  const [showProjectDialog, setShowProjectDialog] = useState(false);
  const [showGroupDialog, setShowGroupDialog] = useState(false);

  const chatsQuery = useInfiniteQuery(sessionsInfiniteQueryOptions(agentId, "chat"));
  const { data: homeSession = null } = useQuery(mainSessionQueryOptions(agentId));
  const { data: groups = [] } = useQuery(groupsQueryOptions);
  const { data: projects = [] } = useQuery(agentProjectsOptions(agentId));
  const { data: inbox } = useQuery(inboxQueryOptions(undefined, 100));
  const activeAgentId = pathname.match(/\/agents\/([^/]+)/)?.[1] ?? agentId;
  const activeGroupId = pathname.match(/\/groups\/([^/]+)/)?.[1] ?? "";
  const activeSessionId = pathname.match(/\/sessions\/([^/]+)/)?.[1] ?? "";
  const activeProjectId = pathname.match(/\/projects\/([^/]+)/)?.[1] ?? "";

  const attentionCount = inbox?.items?.length ?? 0;

  const chatSessions = (chatsQuery.data?.pages.flatMap((page) => page.sessions) ?? [])
    .filter((session) => !session.archived)
    .sort((a, b) => new Date(b.last_active).getTime() - new Date(a.last_active).getTime());

  const conversations = useMemo(() => {
    const agentItems = agents.map((agent, index) => ({
      kind: "agent" as const,
      id: agent.id,
      label: agent.name,
      updatedAt: agent.last_active ?? "",
      index,
    }));
    const groupItems = groups.map((group) => ({
      kind: "group" as const,
      id: group.id,
      label: group.group_name || t("groups.unnamed"),
      updatedAt: group.last_active ?? group.updated_at,
      index: 0,
    }));
    return [...agentItems, ...groupItems].sort((a, b) => {
      if (!a.updatedAt && !b.updatedAt) return a.index - b.index;
      if (!a.updatedAt) return 1;
      if (!b.updatedAt) return -1;
      return new Date(b.updatedAt).getTime() - new Date(a.updatedAt).getTime();
    });
  }, [agents, groups, t]);

  const createTemporarySession = useCallback(async () => {
    const { data } = await sdkCreateSession({
      path: { agentId },
      body: { kind: "chat" },
      throwOnError: true,
    });
    const session = data as ComponentsSession;
    await queryClient.invalidateQueries({ queryKey: ["sessions", agentId] });
    closeMobile();
    void navigate({
      to: "/agents/$agentId/sessions/$sessionId",
      params: { agentId, sessionId: session.id },
    });
  }, [agentId, closeMobile, navigate, queryClient]);

  const openProject = useCallback(
    (projectId: string) => {
      closeMobile();
      void navigate({
        to: "/agents/$agentId/projects/$projectId",
        params: { agentId, projectId },
      });
    },
    [agentId, closeMobile, navigate],
  );

  const deleteProject = useCallback(
    async (projectId: string) => {
      if (!window.confirm(t("sessions.sidebar.deleteProjectConfirm"))) return;
      await sdkDeleteProject({ path: { agentId, projectId }, throwOnError: true });
      await queryClient.invalidateQueries({ queryKey: ["projects", agentId] });
    },
    [agentId, queryClient, t],
  );

  return (
    <div className="flex min-h-0 w-full flex-col overflow-hidden">
      <div className="shrink-0 px-3">
        <SidebarSection title={t("inbox.needsYou")} className="mt-0">
          <SidebarItem
            active={pathname.startsWith("/inbox")}
            icon={<Bell className="size-4" />}
            label={t("inbox.title")}
            badge={
              attentionCount > 0 ? (
                <span className="shrink-0 rounded-full bg-muted px-2 py-0.5 font-mono text-xs text-foreground">
                  {attentionCount}
                </span>
              ) : undefined
            }
            onClick={() => {
              closeMobile();
              void navigate({ to: "/inbox" });
            }}
          />
        </SidebarSection>
      </div>
      <div className="flex-1 overflow-y-auto overflow-x-hidden px-3 pb-2">
        <SidebarSection
          title={t("sessions.sidebar.chats")}
          action={
            <button
              type="button"
              className="grid size-6 place-items-center rounded-lg text-muted-foreground opacity-60 transition-colors hover:bg-foreground/[0.055] hover:text-foreground hover:opacity-100"
              title={t("groups.newGroup")}
              onClick={() => setShowGroupDialog(true)}
            >
              <Users className="size-3.5" />
            </button>
          }
        >
          {conversations.map((item) => {
            const active =
              item.kind === "agent"
                ? activeAgentId === item.id && !activeGroupId
                : activeGroupId === item.id;
            return (
              <SidebarItem
                key={`${item.kind}:${item.id}`}
                active={active}
                icon={
                  item.kind === "agent" ? (
                    <span
                      className="grid size-6 place-items-center rounded-full text-xs font-semibold text-primary-foreground"
                      style={{ background: getAgentColor(item.id, item.index) }}
                    >
                      {item.label[0]?.toUpperCase()}
                    </span>
                  ) : (
                    <Users className="size-4" />
                  )
                }
                label={item.label}
                meta={
                  item.updatedAt ? (
                    <span className="font-mono text-xs">{relativeTime(item.updatedAt)}</span>
                  ) : undefined
                }
                onClick={() => {
                  closeMobile();
                  if (item.kind === "agent") onAgentChange(item.id);
                  else void navigate({ to: "/groups/$groupId", params: { groupId: item.id } });
                }}
              />
            );
          })}
          {conversations.length === 0 && (
            <p className="px-2 py-2 text-xs text-muted-foreground">
              {t("sessions.sidebar.noAgents")}
            </p>
          )}
        </SidebarSection>

        <SidebarSection
          title={t("sessions.sidebar.projects")}
          open={projectsOpen}
          onOpenChange={setProjectsOpen}
          action={
            <button
              type="button"
              className="grid size-6 place-items-center rounded-lg text-muted-foreground opacity-60 transition-colors hover:bg-foreground/[0.055] hover:text-foreground hover:opacity-100"
              title={t("sessions.sidebar.newProject2")}
              onClick={() => setShowProjectDialog(true)}
            >
              <FolderPlus className="size-3.5" />
            </button>
          }
        >
          {(projects as Project[]).map((project) => (
            <SidebarItem
              key={project.id}
              active={activeProjectId === project.id}
              className="group/project"
              icon={<Folder className="size-4" />}
              label={project.name}
              meta={<span className="font-mono text-xs">{relativeTime(project.updated_at)}</span>}
              trailing={
                <span
                  className="grid size-6 place-items-center rounded-lg text-muted-foreground opacity-0 transition-colors hover:bg-card hover:text-foreground group-hover/project:opacity-70"
                  onClick={(event) => {
                    event.stopPropagation();
                    void deleteProject(project.id);
                  }}
                >
                  <MoreHorizontal className="size-3.5" />
                </span>
              }
              onClick={() => openProject(project.id)}
            />
          ))}
        </SidebarSection>

        <SidebarSection
          title={t("sessions.sidebar.threads")}
          open={chatsOpen}
          onOpenChange={setChatsOpen}
          action={
            <button
              type="button"
              className="grid size-6 place-items-center rounded-lg text-muted-foreground opacity-60 transition-colors hover:bg-foreground/[0.055] hover:text-foreground hover:opacity-100"
              title={t("sessions.sidebar.newThread")}
              onClick={() => void createTemporarySession()}
            >
              <MessageSquarePlus className="size-3.5" />
            </button>
          }
        >
          {chatSessions.map((session: Session) => (
            <SidebarItem
              key={session.id}
              active={activeSessionId === session.id}
              label={session.title || t("sessions.untitled")}
              meta={<time className="font-mono text-xs">{relativeTime(session.last_active)}</time>}
              onClick={() => {
                closeMobile();
                void navigate({
                  to: "/agents/$agentId/sessions/$sessionId",
                  params: { agentId, sessionId: session.id },
                });
              }}
            />
          ))}
          {chatsQuery.hasNextPage && (
            <button
              type="button"
              className="w-full px-2 py-1.5 text-left text-xs text-muted-foreground transition-colors hover:text-foreground"
              disabled={chatsQuery.isFetchingNextPage}
              onClick={() => void chatsQuery.fetchNextPage()}
            >
              {t("sessions.sidebar.loadMore")}
            </button>
          )}
          {chatSessions.length === 0 && (
            <p className="px-2 py-2 text-xs text-muted-foreground">
              {t("sessions.sidebar.noThreads")}
            </p>
          )}
        </SidebarSection>
      </div>
      {showProjectDialog && (
        <CreateProjectDialog
          agentId={agentId}
          sessionId={homeSession?.id ?? ""}
          onCreated={() => {
            setShowProjectDialog(false);
            void queryClient.invalidateQueries({ queryKey: ["projects", agentId] });
          }}
          onClose={() => setShowProjectDialog(false)}
        />
      )}
      <CreateGroupDialog open={showGroupDialog} onClose={() => setShowGroupDialog(false)} />
    </div>
  );
}
