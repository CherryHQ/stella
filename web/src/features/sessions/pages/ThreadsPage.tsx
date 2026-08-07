import { useEffect, useMemo, useState } from "react";
import { Link, useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { useInfiniteQuery, useQuery, useQueryClient } from "@tanstack/react-query";
import { MessageSquare, MoreHorizontal, Pencil, Search, Trash2 } from "lucide-react";
import { deleteSession, updateSession } from "@/lib/api-client/sdk.gen";
import { agentsQueryOptions } from "@/lib/queries/agents";
import { agentProjectsOptions } from "@/lib/queries/projects";
import { agentThreadsInfiniteQueryOptions, sortedChats } from "@/lib/queries/sessions";
import { apiErrorMessage } from "@/lib/api-error";
import { useI18n } from "@/lib/i18n";
import { formatTime } from "@/lib/time";
import type { Session } from "@/lib/types";
import { sessionDisplayTitle } from "@/lib/session-title";
import { SessionOriginBadge } from "@/components/SessionOriginBadge";
import { useAppShell } from "@/layouts/AppShell";
import { useToast } from "@/hooks/use-toast";
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
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty";
import { Input } from "@/components/ui/input";
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/menu";
import {
  Select,
  SelectItem,
  SelectPopup,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Spinner } from "@/components/ui/spinner";

const ROUTE = "/_app/agents/$agentId/threads";
/** Home filter value for "threads that belong to the agent itself". */
const AGENT_HOME = "agent";
/** Home filter value for "every home". Never written to the URL. */
const ALL_HOMES = "all";

/**
 * Every chat thread of one agent, agent-level and project alike — the single
 * place the two homes are listed together. Each row still links to its own
 * home, so opening a thread from here lands on the same route the sidebar
 * would have used.
 */
export function ThreadsPage() {
  const { agentId } = useParams({ from: ROUTE });
  const { home, q } = useSearch({ from: ROUTE });
  const navigate = useNavigate();
  const { t } = useI18n();
  const { setHeaderTitle, setHeaderActions } = useAppShell();
  const { showToast } = useToast();
  const queryClient = useQueryClient();

  const { data: agents = [] } = useQuery(agentsQueryOptions);
  const { data: projects = [] } = useQuery(agentProjectsOptions(agentId));
  const agentName = agents.find((agent) => agent.id === agentId)?.name ?? "";
  const projectNames = useMemo(
    () => new Map(projects.map((project) => [project.id, project.name])),
    [projects],
  );

  // The URL only carries a hint; the whitelist is "agent" plus this agent's own
  // project ids, so a stale or forged id degrades to "all" instead of an empty list.
  const selectedHome = home === AGENT_HOME || (home && projectNames.has(home)) ? home : ALL_HOMES;
  const projectFilter =
    selectedHome !== AGENT_HOME && selectedHome !== ALL_HOMES ? selectedHome : undefined;

  const threadsQuery = useInfiniteQuery(agentThreadsInfiniteQueryOptions(agentId, projectFilter));

  const [editingId, setEditingId] = useState("");
  const [draftTitle, setDraftTitle] = useState("");
  const [renaming, setRenaming] = useState(false);
  const [pendingDelete, setPendingDelete] = useState<Session | null>(null);

  useEffect(() => {
    setHeaderTitle(
      <h1 className="truncate text-[15px] font-semibold tracking-[-0.01em]">
        {t("sidebar.conversations")}
      </h1>,
    );
    setHeaderActions(null);
    // The shell outlives this page — a stale title would linger on the next one.
    return () => {
      setHeaderTitle(null);
      setHeaderActions(null);
    };
  }, [setHeaderActions, setHeaderTitle, t]);

  const needle = (q ?? "").trim().toLowerCase();
  const threads = useMemo(() => {
    const loaded = sortedChats(threadsQuery.data?.pages.flatMap((page) => page.sessions) ?? []);
    const scoped =
      selectedHome === AGENT_HOME ? loaded.filter((session) => !session.project_id) : loaded;
    return needle
      ? scoped.filter((session) =>
          // Match what is on screen as well as what is stored: a derived title
          // is the only text the user can actually see to search for.
          `${session.title ?? ""} ${sessionDisplayTitle(session.title, "")}`
            .toLowerCase()
            .includes(needle),
        )
      : scoped;
  }, [needle, selectedHome, threadsQuery.data]);

  function go(next: { home?: string; q?: string }) {
    const merged = { home: selectedHome, q: q ?? "", ...next };
    void navigate({
      to: "/agents/$agentId/threads",
      params: { agentId },
      search: {
        ...(merged.home && merged.home !== ALL_HOMES ? { home: merged.home } : {}),
        ...(merged.q ? { q: merged.q } : {}),
      },
      replace: true,
    });
  }

  function cancelRename() {
    if (renaming) return;
    setEditingId("");
    setDraftTitle("");
  }

  async function rename(session: Session, currentTitle: string) {
    if (renaming) return;
    const title = draftTitle.trim();
    if (!title || title === currentTitle) {
      cancelRename();
      return;
    }
    setRenaming(true);
    try {
      await updateSession({
        path: { agentId, sessionId: session.id },
        body: { title },
        throwOnError: true,
      });
      await queryClient.invalidateQueries({ queryKey: ["sessions", agentId] });
      setEditingId("");
      setDraftTitle("");
    } catch (error) {
      showToast(apiErrorMessage(error, t("sessions.sidebar.renameFailed")), "error");
      setDraftTitle(currentTitle);
    } finally {
      setRenaming(false);
    }
  }

  async function remove(session: Session) {
    try {
      await deleteSession({ path: { agentId, sessionId: session.id }, throwOnError: true });
      await queryClient.invalidateQueries({ queryKey: ["sessions", agentId] });
    } catch (error) {
      showToast(apiErrorMessage(error, t("common.error")), "error");
    } finally {
      setPendingDelete(null);
    }
  }

  const homeItems = useMemo(
    () => [
      { value: ALL_HOMES, label: t("threads.homeAll") },
      { value: AGENT_HOME, label: agentName || t("threads.homeAgent") },
      ...projects.map((project) => ({ value: project.id, label: project.name })),
    ],
    [agentName, projects, t],
  );

  return (
    <div className="flex h-full min-h-0 flex-col overflow-hidden">
      <div className="flex flex-wrap items-center gap-2 border-b p-3 sm:px-4">
        <InputGroup className="w-full sm:max-w-xs">
          <InputGroupAddon>
            <Search />
          </InputGroupAddon>
          <InputGroupInput
            nativeInput
            type="search"
            value={q ?? ""}
            onChange={(event) => go({ q: (event.target as HTMLInputElement).value })}
            placeholder={t("threads.searchPlaceholder")}
          />
        </InputGroup>
        <Select
          items={homeItems}
          value={selectedHome}
          onValueChange={(value) => go({ home: (value ?? ALL_HOMES) as string })}
        >
          <SelectTrigger aria-label={t("threads.homeFilter")} className="w-full sm:w-56">
            <SelectValue />
          </SelectTrigger>
          <SelectPopup>
            {homeItems.map((item) => (
              <SelectItem key={item.value} value={item.value}>
                {item.label}
              </SelectItem>
            ))}
          </SelectPopup>
        </Select>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto p-3 sm:p-4">
        {threadsQuery.isLoading ? (
          <div className="flex h-48 items-center justify-center">
            <Spinner />
          </div>
        ) : threads.length === 0 ? (
          <Empty>
            <EmptyHeader>
              <EmptyMedia variant="icon">
                <MessageSquare />
              </EmptyMedia>
              <EmptyTitle>{t("threads.emptyTitle")}</EmptyTitle>
              <EmptyDescription>
                {needle ? t("threads.emptySearch") : t("threads.emptyDesc")}
              </EmptyDescription>
            </EmptyHeader>
          </Empty>
        ) : (
          <div className="divide-y divide-border overflow-hidden rounded-lg border">
            {threads.map((session) => {
              const label = sessionDisplayTitle(session.title, t("sessions.untitled"));
              const projectId = session.project_id;
              // Only a project home discriminates. On an agent's own thread list
              // the agent name is on every row, and a badge that never varies is
              // read as chrome — worse here, because it sits beside the origin
              // badge, which does vary and is the one worth seeing.
              const homeLabel = projectId
                ? (projectNames.get(projectId) ?? t("sidebar.projects"))
                : null;
              return (
                <Link
                  key={session.id}
                  className="group/thread flex items-center gap-3 p-3 transition-colors hover:bg-muted/40"
                  to={
                    projectId
                      ? "/agents/$agentId/projects/$projectId/sessions/$sessionId"
                      : "/agents/$agentId/sessions/$sessionId"
                  }
                  params={
                    (projectId
                      ? { agentId, projectId, sessionId: session.id }
                      : { agentId, sessionId: session.id }) as never
                  }
                >
                  <MessageSquare className="size-4 shrink-0 text-muted-foreground" />
                  <span className="min-w-0 flex-1 truncate text-sm">
                    {editingId === session.id ? (
                      <Input
                        autoFocus
                        unstyled
                        size="sm"
                        className="w-full min-w-0"
                        disabled={renaming}
                        value={draftTitle}
                        onBlur={() => void rename(session, label)}
                        onChange={(event) => setDraftTitle(event.target.value)}
                        onClick={(event) => {
                          event.preventDefault();
                          event.stopPropagation();
                        }}
                        onFocus={(event) => event.currentTarget.select()}
                        onKeyDown={(event) => {
                          if (event.key === "Enter") {
                            event.preventDefault();
                            void rename(session, label);
                          }
                          if (event.key === "Escape") {
                            event.preventDefault();
                            cancelRename();
                          }
                        }}
                      />
                    ) : (
                      label
                    )}
                  </span>
                  <SessionOriginBadge session={session} />
                  {homeLabel && (
                    <Badge variant="outline" size="sm" className="max-sm:hidden">
                      {homeLabel}
                    </Badge>
                  )}
                  <time className="shrink-0 text-xs text-muted-foreground">
                    {formatTime(session.last_active)}
                  </time>
                  {editingId !== session.id && (
                    <DropdownMenu>
                      <DropdownMenuTrigger
                        // The row is a link, so a nested <button> would be invalid
                        // markup; Base UI supplies the role and keyboard handling.
                        nativeButton={false}
                        render={
                          <span
                            aria-label={t("threads.rowActions")}
                            className="grid size-6 shrink-0 place-items-center rounded-lg text-muted-foreground transition-colors hover:bg-card hover:text-foreground"
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
                            setEditingId(session.id);
                            setDraftTitle(label);
                          }}
                        >
                          <Pencil className="size-4" />
                          {t("sessions.sidebar.renameThread")}
                        </DropdownMenuItem>
                        <DropdownMenuItem
                          onMouseDown={(event) => {
                            event.preventDefault();
                            setPendingDelete(session);
                          }}
                        >
                          <Trash2 className="size-4" />
                          {t("common.delete")}
                        </DropdownMenuItem>
                      </DropdownMenuContent>
                    </DropdownMenu>
                  )}
                </Link>
              );
            })}
          </div>
        )}

        {threadsQuery.hasNextPage && (
          <div className="flex justify-center py-4">
            <Button
              variant="outline"
              size="sm"
              loading={threadsQuery.isFetchingNextPage}
              onClick={() => void threadsQuery.fetchNextPage()}
            >
              {t("sidebar.showMore")}
            </Button>
          </div>
        )}
      </div>

      <AlertDialog open={!!pendingDelete} onOpenChange={(open) => !open && setPendingDelete(null)}>
        <AlertDialogPopup>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("sessions.sidebar.deleteThreadConfirm")}</AlertDialogTitle>
            <AlertDialogDescription>
              {pendingDelete?.title || t("sessions.untitled")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogClose render={<Button variant="ghost" />}>
              {t("common.cancel")}
            </AlertDialogClose>
            <Button
              variant="destructive"
              onClick={() => pendingDelete && void remove(pendingDelete)}
            >
              {t("common.delete")}
            </Button>
          </AlertDialogFooter>
        </AlertDialogPopup>
      </AlertDialog>
    </div>
  );
}
