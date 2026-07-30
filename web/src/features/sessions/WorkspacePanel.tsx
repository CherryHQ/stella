import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import {
  FileTree as FileTreeComponent,
  useFileTree,
  useFileTreeSelection,
} from "@pierre/trees/react";
import { themeToTreeStyles, type TreeThemeInput } from "@pierre/trees";
import {
  FilePlus,
  FolderPlus,
  Trash2,
  RefreshCw,
  X,
  Copy,
  ChevronRight,
  ChevronDown,
  Eye,
  EyeOff,
} from "lucide-react";
import {
  createShare as sdkCreateShare,
  createWorkspaceFile,
  deleteWorkspaceFile,
  getSessionWorkspace,
  getWorkspaceFileContent,
  moveWorkspaceFile,
  revokeShare as sdkRevokeShare,
  updateWorkspaceFileContent,
} from "@/lib/api-client/sdk.gen";
import type { Workspace } from "@/lib/types";
import { cn } from "@/lib/utils";
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
import { FileViewer } from "./FileViewer";
import { isNonTextFile, fetchBlobUrl, mimeTypeForPath } from "./fileUtils";
import { useI18n } from "@/lib/i18n";

function buildTheme(): TreeThemeInput {
  const style = getComputedStyle(document.documentElement);
  const get = (v: string): string | undefined => {
    const val = style.getPropertyValue(v).trim();
    return val || undefined;
  };
  const isDark = document.documentElement.classList.contains("dark");

  const colors: Record<string, string> = {};
  const set = (key: string, val: string | undefined) => {
    if (val) colors[key] = val;
  };

  set("input.background", get("--muted"));
  set("input.border", get("--border"));
  set("sideBar.background", get("--background"));
  set("sideBar.foreground", get("--foreground"));
  set("sideBar.border", get("--border"));
  set("list.hoverBackground", get("--muted"));
  set("list.activeSelectionBackground", get("--accent"));
  set("list.activeSelectionForeground", get("--primary"));

  return {
    type: isDark ? "dark" : "light",
    bg: get("--background"),
    fg: get("--foreground"),
    colors,
  };
}

interface Props {
  agentID: string;
  sessionID: string;
  workspace: Workspace | null;
  workspaceLoading: boolean;
  onReload: (sid: string, path?: string) => Promise<void>;
  projectDir?: string;
}

type ViewMode = "tree" | "viewer";

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes}B`;
  const units = ["KB", "MB", "GB", "TB"];
  let value = bytes / 1024;
  let unit = units[0];
  for (let i = 1; i < units.length && value >= 1024; i++) {
    value /= 1024;
    unit = units[i];
  }
  return `${value >= 10 ? value.toFixed(0) : value.toFixed(1)}${unit}`;
}

function isDirectoryPath(path: string): boolean {
  return path.endsWith("/");
}

function basename(path: string): string {
  const trimmed = isDirectoryPath(path) ? path.slice(0, -1) : path;
  return trimmed.split("/").pop() ?? trimmed;
}

// Hidden entries are dot-prefixed at any level. In the shared scope these are
// CLI-managed state (XDG directories, tool logins) that every agent of the user
// depends on; keep them out of casual reach of the panel's rename/delete
// actions unless the user explicitly opts in.
function isHiddenPath(path: string): boolean {
  const trimmed = isDirectoryPath(path) ? path.slice(0, -1) : path;
  return trimmed.split("/").some((segment) => segment.startsWith("."));
}

function isShareableArtifact(path: string): boolean {
  return /\.(html?|mdx?|markdown|png|jpe?g|gif|webp|svg|avif|bmp|ico|pdf)$/i.test(path);
}

type Scope = "agent" | "user";

interface ViewerState {
  path: string;
  scope: Scope;
  content: string;
  language: string;
  loading: boolean;
  saving: boolean;
}

export function WorkspacePanel({
  agentID,
  sessionID,
  workspace,
  workspaceLoading,
  onReload,
  projectDir,
}: Props) {
  const { t } = useI18n();
  const [mode, setMode] = useState<ViewMode>("tree");
  const [viewer, setViewer] = useState<ViewerState | null>(null);

  const agentReload = useCallback(() => {
    onReload(sessionID, projectDir || undefined).catch(console.error);
  }, [sessionID, onReload, projectDir]);

  const openFile = useCallback(
    async (path: string, scope: Scope) => {
      setViewer({ path, scope, content: "", language: "", loading: true, saving: false });
      setMode("viewer");
      if (isNonTextFile(path)) {
        setViewer((v) => (v ? { ...v, loading: false } : null));
        return;
      }
      try {
        const { data } = await getWorkspaceFileContent({
          path: { agentId: agentID, sessionId: sessionID },
          query: { path, scope },
          throwOnError: true,
        });
        const file = data as { content?: string; language?: string };
        setViewer((v) =>
          v && v.path === path && v.scope === scope
            ? { ...v, content: file.content ?? "", language: file.language ?? "", loading: false }
            : v,
        );
      } catch (e) {
        console.error(e);
        setViewer(null);
        setMode("tree");
      }
    },
    [agentID, sessionID],
  );

  const saveFile = useCallback(
    async (content: string) => {
      if (!viewer) return;
      setViewer((v) => (v ? { ...v, saving: true } : null));
      try {
        await updateWorkspaceFileContent({
          path: { agentId: agentID, sessionId: sessionID },
          query: { scope: viewer.scope },
          body: { path: viewer.path, content },
          throwOnError: true,
        });
        setViewer((v) => (v ? { ...v, content, saving: false } : null));
      } catch (e) {
        console.error(e);
        setViewer((v) => (v ? { ...v, saving: false } : null));
      }
    },
    [viewer, agentID, sessionID],
  );

  // Close the viewer when the file it shows is deleted in a section.
  const onDeleted = useCallback(
    (path: string, scope: Scope) => {
      if (viewer?.path === path && viewer.scope === scope) {
        setViewer(null);
        setMode("tree");
      }
    },
    [viewer],
  );

  const goBack = useCallback(() => {
    setMode("tree");
    setViewer(null);
  }, []);

  // Reset mode when session changes
  useEffect(() => {
    setMode("tree");
    setViewer(null);
  }, [sessionID]);

  if (!sessionID) {
    return (
      <div className="flex h-full w-full min-w-0 flex-col overflow-hidden bg-sidebar/80">
        <div className="flex h-12 flex-shrink-0 items-center justify-between border-b border-border/70 px-4">
          <span className="text-xs font-mono text-muted-foreground">
            {t("sessions.workspace.title")}
          </span>
        </div>
        <div className="flex-1 flex items-center justify-center">
          <p className="text-xs text-muted-foreground font-mono">
            Select a session to see its workspace
          </p>
        </div>
      </div>
    );
  }

  if (mode === "viewer" && viewer) {
    return (
      <FileViewer
        path={viewer.path}
        content={viewer.content}
        language={viewer.language}
        loading={viewer.loading}
        saving={viewer.saving}
        agentID={agentID}
        sessionID={sessionID}
        scope={viewer.scope}
        onBack={goBack}
        onSave={saveFile}
      />
    );
  }

  // Two stacked roots, matching what users actually consume: the agent's
  // private workspace ($HOME) and the shared deliverables area
  // ($STELLA_ASSETS_DIR). The shared section is rooted at assets/ rather than
  // the whole managed principal root so CLI-managed state never becomes a
  // file-manager target. The agent root is driven by the parent (it owns
  // project-dir derivation); the shared root self-loads.
  return (
    <div className="flex h-full w-full min-w-0 flex-col overflow-hidden bg-sidebar/80">
      <WorkspaceScopeSection
        scope="agent"
        label={t("sessions.workspace.agentHome")}
        hint={t("sessions.workspace.agentHomeHint")}
        agentID={agentID}
        sessionID={sessionID}
        workspace={workspace}
        loading={workspaceLoading}
        onReload={agentReload}
        onOpenFile={openFile}
        onDeleted={onDeleted}
        rootDir={projectDir}
      />
      <WorkspaceScopeSection
        scope="user"
        label={t("sessions.workspace.sharedData")}
        hint={t("sessions.workspace.sharedDataHint")}
        agentID={agentID}
        sessionID={sessionID}
        selfLoad
        rootDir="assets"
        onOpenFile={openFile}
        onDeleted={onDeleted}
      />
    </div>
  );
}

interface WorkspaceScopeSectionProps {
  scope: Scope;
  label: string;
  hint: string;
  agentID: string;
  sessionID: string;
  workspace?: Workspace | null;
  loading?: boolean;
  onReload?: () => void;
  // selfLoad: fetch this scope's tree internally instead of taking it from props
  // (used for the shared user-data root the parent does not track).
  selfLoad?: boolean;
  onOpenFile: (path: string, scope: Scope) => void;
  onDeleted: (path: string, scope: Scope) => void;
  // rootDir roots the section at a subdirectory of the scope: the tree is
  // fetched, displayed, and created relative to it, and paths outside it never
  // appear. The agent section uses the project directory; the shared section
  // uses the assets area.
  rootDir?: string;
}

function WorkspaceScopeSection({
  scope,
  label,
  hint,
  agentID,
  sessionID,
  workspace: providedWorkspace,
  loading: providedLoading,
  onReload: providedReload,
  selfLoad,
  onOpenFile,
  onDeleted,
  rootDir,
}: WorkspaceScopeSectionProps) {
  const { t } = useI18n();
  const [collapsed, setCollapsed] = useState(false);
  const [showHidden, setShowHidden] = useState(false);
  const [newItemType, setNewItemType] = useState<"file" | "dir" | null>(null);
  const [newItemName, setNewItemName] = useState("");
  const [selectedPath, setSelectedPath] = useState<string | null>(null);

  // Self-loaded scope state (only used when selfLoad).
  const [selfWorkspace, setSelfWorkspace] = useState<Workspace | null>(null);
  const [selfLoading, setSelfLoading] = useState(false);
  // Monotonic token: only the most recent load may write state, so a slow
  // response from a previous session/scope can't clobber the current view.
  const loadToken = useRef(0);
  const loadSelf = useCallback(async () => {
    const token = ++loadToken.current;
    setSelfLoading(true);
    try {
      const { data } = await getSessionWorkspace({
        path: { agentId: agentID, sessionId: sessionID },
        query: { show_hidden: true, depth: 2, scope, ...(rootDir ? { path: rootDir } : {}) },
        throwOnError: true,
      });
      if (token === loadToken.current) setSelfWorkspace(data);
    } catch {
      // Also reached when rootDir does not exist yet (e.g. no assets have been
      // produced); the section renders as empty rather than as an error.
      if (token === loadToken.current) setSelfWorkspace(null);
    } finally {
      if (token === loadToken.current) setSelfLoading(false);
    }
  }, [agentID, sessionID, scope, rootDir]);
  useEffect(() => {
    if (!selfLoad) return;
    // Drop any stale tree before the new session's load lands.
    setSelfWorkspace(null);
    void loadSelf();
  }, [selfLoad, loadSelf]);

  const workspace = selfLoad ? selfWorkspace : (providedWorkspace ?? null);
  const loading = selfLoad ? selfLoading : Boolean(providedLoading);
  const reload = useCallback(() => {
    if (selfLoad) {
      void loadSelf();
    } else {
      providedReload?.();
    }
    setSelectedPath(null);
  }, [selfLoad, loadSelf, providedReload]);

  const createItem = useCallback(async () => {
    const name = newItemName.trim();
    if (!name) return;
    const fullPath = rootDir ? `${rootDir}/${name}` : name;
    await createWorkspaceFile({
      path: { agentId: agentID, sessionId: sessionID },
      query: { scope },
      body: { path: fullPath, is_dir: newItemType === "dir" },
      throwOnError: true,
    });
    reload();
    setNewItemType(null);
    setNewItemName("");
  }, [agentID, sessionID, scope, newItemName, newItemType, reload, rootDir]);

  const deleteItem = useCallback(
    async (path: string) => {
      if (!confirm(`Delete "${path}"?`)) return;
      await deleteWorkspaceFile({
        path: { agentId: agentID, sessionId: sessionID },
        query: { scope },
        body: { path },
        throwOnError: true,
      });
      reload();
      if (selectedPath === path) setSelectedPath(null);
      onDeleted(path, scope);
    },
    [agentID, sessionID, scope, selectedPath, reload, onDeleted],
  );

  // Reset transient state when the session changes.
  useEffect(() => {
    setNewItemType(null);
    setNewItemName("");
    setSelectedPath(null);
  }, [sessionID]);

  // Count what the tree will actually show: paths under rootDir, minus hidden
  // entries unless the user opted in. Sizing and the empty state key off this,
  // so a section whose only content is filtered-out dotfiles reads as empty.
  const prefix = rootDir ? `${rootDir}/` : "";
  const visibleCount = useMemo(
    () =>
      (workspace?.paths ?? []).filter((p) => {
        const display = prefix ? (p.startsWith(prefix) ? p.slice(prefix.length) : "") : p;
        return display !== "" && (showHidden || !isHiddenPath(display));
      }).length,
    [workspace?.paths, prefix, showHidden],
  );
  const fileCount = workspace?.total_files ?? 0;
  const dirCount = workspace?.total_dirs ?? 0;
  // Show the path the agent sees inside its sandbox (e.g. /workspace, /user),
  // not the host disk path; fall back to the host root otherwise.
  const scopeRoot = workspace?.sandbox_root || workspace?.root || "";
  const displayRoot = scopeRoot && rootDir ? `${scopeRoot}/${rootDir}` : scopeRoot;
  const stats = `${fileCount.toLocaleString()} files · ${dirCount.toLocaleString()} folders · ${formatBytes(workspace?.total_bytes ?? 0)}`;

  // An empty section shrinks to its header strip so the section with content
  // gets the panel height, instead of an even split around an "Empty" label.
  const compact = collapsed || (!loading && visibleCount === 0 && newItemType === null);

  return (
    <section
      className={cn(
        "flex min-w-0 flex-col overflow-hidden border-b border-border/70",
        compact ? "flex-none" : "min-h-0 flex-1",
      )}
    >
      {/* Section header */}
      <div className="flex min-h-9 flex-shrink-0 items-center justify-between gap-2 overflow-hidden px-2 py-1.5">
        <button
          type="button"
          onClick={() => setCollapsed((c) => !c)}
          className="flex min-w-0 flex-1 items-center gap-1.5 text-left"
          title={displayRoot || hint}
        >
          {collapsed ? (
            <ChevronRight className="size-3.5 shrink-0 text-muted-foreground" />
          ) : (
            <ChevronDown className="size-3.5 shrink-0 text-muted-foreground" />
          )}
          <span className="shrink-0 truncate text-xs font-semibold text-foreground">{label}</span>
          <span className="min-w-0 flex-1 truncate font-mono text-[10px] text-muted-foreground/70">
            {displayRoot || hint}
          </span>
        </button>
        <div className="flex shrink-0 items-center gap-0">
          <Button
            variant="ghost"
            size="xs"
            onClick={() => setShowHidden((v) => !v)}
            title={t(
              showHidden ? "sessions.workspace.hideHidden" : "sessions.workspace.showHidden",
            )}
            className="px-1 h-6"
          >
            {showHidden ? <Eye className="w-3.5 h-3.5" /> : <EyeOff className="w-3.5 h-3.5" />}
          </Button>
          <Button
            variant="ghost"
            size="xs"
            onClick={() => {
              setCollapsed(false);
              setNewItemType("file");
            }}
            title={t("sessions.workspace.newFile")}
            className="px-1 h-6"
          >
            <FilePlus className="w-3.5 h-3.5" />
          </Button>
          <Button
            variant="ghost"
            size="xs"
            onClick={() => {
              setCollapsed(false);
              setNewItemType("dir");
            }}
            title={t("sessions.workspace.newFolder")}
            className="px-1 h-6"
          >
            <FolderPlus className="w-3.5 h-3.5" />
          </Button>
          <Button
            variant="ghost"
            size="xs"
            disabled={!selectedPath}
            onClick={() => selectedPath && deleteItem(selectedPath).catch(console.error)}
            title={t("sessions.workspace.deleteSelected")}
            className={cn(
              "px-1 h-6",
              selectedPath
                ? "text-destructive hover:bg-destructive/10"
                : "opacity-30 cursor-not-allowed",
            )}
          >
            <Trash2 className="w-3.5 h-3.5" />
          </Button>
          <Button
            variant="ghost"
            size="xs"
            onClick={reload}
            disabled={loading}
            title={t("sessions.workspace.refresh")}
            className="px-1 h-6"
          >
            <RefreshCw className={cn("w-3.5 h-3.5", loading && "animate-spin")} />
          </Button>
        </div>
      </div>

      {!collapsed && (
        <>
          {/* New item form */}
          {newItemType !== null && (
            <div className="min-w-0 flex-shrink-0 border-t border-border px-2 py-1.5">
              <form
                onSubmit={(e) => {
                  e.preventDefault();
                  createItem().catch(console.error);
                }}
                className="flex min-w-0 items-center gap-1.5"
              >
                <span className="text-xs font-mono text-muted-foreground">
                  {newItemType === "dir" ? "dir" : "file"}:
                </span>
                <input
                  autoFocus
                  value={newItemName}
                  onChange={(e) => setNewItemName(e.target.value)}
                  onKeyDown={(e) =>
                    e.key === "Escape" && (setNewItemType(null), setNewItemName(""))
                  }
                  className="h-6 min-w-0 flex-1 rounded border border-border bg-background px-1.5 py-0.5 font-mono text-xs focus:border-primary/60 focus:outline-none"
                  placeholder="name..."
                  autoComplete="off"
                />
                <Button type="submit" size="xs" className="h-6 min-h-0 text-xs">
                  {t("common.add")}
                </Button>
                <Button
                  type="button"
                  variant="ghost"
                  size="icon-xs"
                  onClick={() => {
                    setNewItemType(null);
                    setNewItemName("");
                  }}
                  className="text-muted-foreground"
                >
                  <X className="w-3 h-3" />
                </Button>
              </form>
            </div>
          )}

          {/* Empty state (also covers a missing rootDir, e.g. no assets yet) */}
          {!loading && visibleCount === 0 && !newItemType && (
            <div className="px-4 py-2 text-center">
              <p className="text-xs font-mono text-muted-foreground/40">
                {t("sessions.workspace.empty")}
              </p>
            </div>
          )}

          {/* File tree */}
          {!loading && workspace && visibleCount > 0 && (
            <div className="min-w-0 flex-1 overflow-hidden" title={`${displayRoot}\n${stats}`}>
              <TreeWithSearch
                agentID={agentID}
                sessionID={sessionID}
                scope={scope}
                workspace={workspace}
                onReload={reload}
                onOpenFile={(p) => onOpenFile(p, scope)}
                onSelectedPath={setSelectedPath}
                onDelete={deleteItem}
                onNewFile={() => setNewItemType("file")}
                onNewFolder={() => setNewItemType("dir")}
                rootDir={rootDir}
                showHidden={showHidden}
              />
            </div>
          )}

          {/* Loading state */}
          {loading && !workspace && (
            <div className="flex flex-1 items-center justify-center py-6">
              <div className="w-4 h-4 border border-muted-foreground/30 border-t-muted-foreground rounded-full animate-spin" />
            </div>
          )}
        </>
      )}
    </section>
  );
}

interface ArtifactShareDialogProps {
  path: string | null;
  agentID: string;
  sessionID: string;
  scope: Scope;
  onClose: () => void;
}

function ArtifactShareDialog({
  path,
  agentID,
  sessionID,
  scope,
  onClose,
}: ArtifactShareDialogProps) {
  const { t } = useI18n();
  const expirationOptions = [
    { value: "1h", label: t("sessions.workspace.1hour") },
    { value: "1d", label: t("sessions.workspace.1day") },
    { value: "7d", label: t("sessions.workspace.7days") },
    { value: "never", label: t("sessions.workspace.never") },
  ];
  const [expiresIn, setExpiresIn] = useState("7d");
  const [creating, setCreating] = useState(false);
  const [revoking, setRevoking] = useState(false);
  const [share, setShare] = useState<{ id: string; url: string } | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!path) {
      setShare(null);
      setError(null);
      setExpiresIn("7d");
    }
  }, [path]);

  const createShare = useCallback(async () => {
    if (!path) return;
    setCreating(true);
    setError(null);
    try {
      const { data: result } = await sdkCreateShare({
        body: {
          source: "artifact",
          agent_id: agentID,
          session_id: sessionID,
          path,
          scope,
          expires_in: expiresIn as "1h" | "1d" | "7d" | "never",
        },
        throwOnError: true,
      });
      setShare({ id: result.id, url: result.url });
      await navigator.clipboard?.writeText(result.url).catch(() => undefined);
    } catch (e) {
      setError(e instanceof Error ? e.message : t("sessions.workspace.shareFailed"));
    } finally {
      setCreating(false);
    }
  }, [agentID, expiresIn, path, sessionID, scope]);

  const revokeShare = useCallback(async () => {
    if (!share) return;
    setRevoking(true);
    setError(null);
    try {
      await sdkRevokeShare({ path: { id: share.id }, throwOnError: true });
      setShare(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : t("sessions.workspace.revokeFailed"));
    } finally {
      setRevoking(false);
    }
  }, [share]);

  const copyShare = useCallback(async () => {
    if (share) await navigator.clipboard?.writeText(share.url);
  }, [share]);

  return (
    <Dialog open={Boolean(path)} onOpenChange={(open) => !open && onClose()}>
      <DialogPopup>
        <DialogHeader>
          <DialogTitle>{t("sessions.workspace.shareArtifact")}</DialogTitle>
          <DialogDescription>
            {t("sessions.workspace.shareDesc", {
              file: path ? basename(path) : t("sessions.workspace.thisFile"),
            })}
          </DialogDescription>
        </DialogHeader>
        <DialogPanel className="space-y-4">
          <div className="space-y-2">
            <label
              className="text-xs font-medium text-muted-foreground"
              htmlFor="artifact-share-expiry"
            >
              {t("sessions.workspace.expiration")}
            </label>
            <select
              id="artifact-share-expiry"
              className="w-full rounded-lg border border-input bg-background px-3 py-2 text-sm"
              disabled={creating || Boolean(share)}
              value={expiresIn}
              onChange={(e) => setExpiresIn(e.currentTarget.value)}
            >
              {expirationOptions.map((option) => (
                <option key={option.value} value={option.value}>
                  {option.label}
                </option>
              ))}
            </select>
          </div>
          {share && (
            <div className="rounded-lg border bg-muted/40 p-3">
              <div className="mb-2 text-xs font-medium text-muted-foreground">
                {t("sessions.workspace.publicUrl")}
              </div>
              <div className="flex items-center gap-2">
                <input
                  readOnly
                  className="min-w-0 flex-1 rounded-md border bg-background px-2 py-1.5 text-xs font-mono"
                  value={share.url}
                />
                <Button size="sm" variant="outline" onClick={copyShare}>
                  <Copy className="size-3.5" />
                  {t("common.copy")}
                </Button>
              </div>
            </div>
          )}
          {error && <p className="text-sm text-destructive">{error}</p>}
        </DialogPanel>
        <DialogFooter>
          {share ? (
            <Button variant="destructive" disabled={revoking} onClick={revokeShare}>
              {revoking ? t("sessions.workspace.revoking") : t("sessions.workspace.revokeLink")}
            </Button>
          ) : (
            <Button disabled={creating} onClick={createShare}>
              {creating ? t("sessions.workspace.creating") : t("sessions.workspace.createLink")}
            </Button>
          )}
          <Button variant="ghost" onClick={onClose}>
            {t("common.close")}
          </Button>
        </DialogFooter>
      </DialogPopup>
    </Dialog>
  );
}

interface TreeWithSearchProps {
  agentID: string;
  sessionID: string;
  scope: Scope;
  workspace: Workspace;
  onReload: () => void;
  onOpenFile: (path: string) => void;
  onSelectedPath: (path: string | null) => void;
  onDelete: (path: string) => Promise<void>;
  onNewFile: () => void;
  onNewFolder: () => void;
  rootDir?: string;
  showHidden: boolean;
}

const ctxItemStyle: React.CSSProperties = {
  display: "flex",
  alignItems: "center",
  gap: 8,
  width: "100%",
  padding: "5px 10px",
  border: "none",
  background: "none",
  color: "inherit",
  fontSize: 12,
  fontFamily: "var(--font-mono, monospace)",
  cursor: "pointer",
  textAlign: "left",
  borderRadius: 4,
};

function TreeWithSearch({
  agentID,
  sessionID,
  scope,
  workspace,
  onReload,
  onOpenFile,
  onSelectedPath,
  onDelete,
  onNewFile,
  onNewFolder,
  rootDir,
  showHidden,
}: TreeWithSearchProps) {
  const { t } = useI18n();
  const enc = encodeURIComponent(sessionID);
  const agentEnc = encodeURIComponent(agentID);
  const theme = useMemo(() => buildTheme(), []);
  const themeStyles = useMemo(() => themeToTreeStyles(theme), [theme]);
  const [sharePath, setSharePath] = useState<string | null>(null);

  // With a rootDir, paths outside it are dropped rather than shown raw: the
  // section is rooted there and everything else is out of scope.
  const prefix = rootDir ? rootDir + "/" : "";
  const toDisplay = useCallback(
    (p: string) => (prefix ? (p.startsWith(prefix) ? p.slice(prefix.length) : "") : p),
    [prefix],
  );
  const toApi = useCallback((p: string) => (prefix ? prefix + p : p), [prefix]);

  const displayPaths = useMemo(
    () =>
      (workspace.paths ?? [])
        .map((p) => toDisplay(p))
        .filter((p) => p && (showHidden || !isHiddenPath(p))),
    [workspace.paths, toDisplay, showHidden],
  );

  const loadedPathSet = useRef(new Set(displayPaths));
  const loadedDirSet = useRef(new Set([""]));
  const loadingDirSet = useRef(new Set<string>());
  const { model } = useFileTree({
    paths: displayPaths,
    initialExpansion: "closed",
    search: true,
    icons: "standard",
    density: "compact",
    composition: {
      contextMenu: {
        enabled: true,
        triggerMode: "both",
        buttonVisibility: "when-needed",
      },
    },
    renaming: {
      onRename: async ({ sourcePath, destinationPath }) => {
        try {
          await moveWorkspaceFile({
            path: { agentId: agentID, sessionId: sessionID },
            query: { scope },
            body: { path: toApi(sourcePath), new_path: toApi(destinationPath) },
            throwOnError: true,
          });
        } catch (e: unknown) {
          const msg = e instanceof Error ? e.message : "Rename failed";
          console.error("rename:", msg);
          model.move(destinationPath, sourcePath);
        }
      },
      onError: (message: string) => console.error("rename error:", message),
    },
    dragAndDrop: {
      onDropComplete: async ({ draggedPaths, target }) => {
        try {
          for (const src of draggedPaths) {
            const filename = basename(src);
            const destDir = (target.directoryPath ?? "").replace(/\/$/, "");
            const newPath = destDir ? `${destDir}/${filename}` : filename;
            await moveWorkspaceFile({
              path: { agentId: agentID, sessionId: sessionID },
              query: { scope },
              body: { path: toApi(src), new_path: toApi(newPath) },
              throwOnError: true,
            });
          }
        } catch (e: unknown) {
          const msg = e instanceof Error ? e.message : "Move failed";
          console.error("drop:", msg);
          onReload();
        }
      },
      onDropError: (message: string) => console.error("drop error:", message),
    },
  });

  useEffect(() => {
    loadedPathSet.current = new Set(displayPaths);
    loadedDirSet.current = new Set([""]);
    loadingDirSet.current = new Set();
    model.resetPaths(displayPaths, { initialExpandedPaths: [] });
  }, [model, displayPaths]);

  const loadDirectory = useCallback(
    async (path: string) => {
      const dir = path.replace(/\/$/, "");
      if (loadedDirSet.current.has(dir) || loadingDirSet.current.has(dir)) return;
      loadingDirSet.current.add(dir);
      try {
        const apiDir = toApi(dir);
        const { data } = await getSessionWorkspace({
          path: { agentId: agentID, sessionId: sessionID },
          query: { show_hidden: true, depth: 2, path: apiDir, scope },
          throwOnError: true,
        });
        const added: string[] = [];
        for (const rawPath of data.paths ?? []) {
          const displayPath = toDisplay(rawPath);
          if (!displayPath || loadedPathSet.current.has(displayPath)) continue;
          if (!showHidden && isHiddenPath(displayPath)) continue;
          loadedPathSet.current.add(displayPath);
          added.push(displayPath);
        }
        for (const nextPath of added) model.add(nextPath);
        loadedDirSet.current.add(dir);
      } catch (e) {
        console.error(e);
      } finally {
        loadingDirSet.current.delete(dir);
      }
    },
    [agentID, sessionID, scope, model, toApi, toDisplay, showHidden],
  );

  const selectedPaths = useFileTreeSelection(model);

  const firstSelected = selectedPaths[0] ?? null;
  const firstSelectedApi = firstSelected ? toApi(firstSelected) : null;
  useEffect(() => {
    onSelectedPath(firstSelectedApi);
  }, [firstSelectedApi, onSelectedPath]);

  return (
    <div className="flex h-full min-w-0 flex-col overflow-hidden">
      <ArtifactShareDialog
        path={sharePath}
        agentID={agentID}
        sessionID={sessionID}
        scope={scope}
        onClose={() => setSharePath(null)}
      />
      <div className="min-w-0 flex-1 overflow-hidden">
        <FileTreeComponent
          model={model}
          className="min-w-0 max-w-full overflow-hidden"
          style={{ ...themeStyles, width: "100%", height: "100%" }}
          renderContextMenu={(item, context) => {
            const MENU_W = 160;
            const MENU_H = 240;
            const { anchorRect } = context;
            const isDir = isDirectoryPath(item.path);
            const apiPath = toApi(item.path);
            const canShare = !isDir && isShareableArtifact(apiPath);
            const left =
              anchorRect.right + MENU_W > window.innerWidth
                ? Math.max(0, anchorRect.left - MENU_W)
                : anchorRect.right;
            const top =
              anchorRect.top + MENU_H > window.innerHeight
                ? Math.max(0, anchorRect.bottom - MENU_H)
                : anchorRect.top;
            return createPortal(
              <div
                data-file-tree-context-menu-root="true"
                style={{
                  position: "fixed",
                  top,
                  left,
                  zIndex: 9999,
                  borderRadius: 8,
                  border: "1px solid var(--border)",
                  background: "var(--popover)",
                  color: "var(--popover-foreground)",
                  padding: "4px 0",
                  minWidth: MENU_W,
                  boxShadow: "var(--shadow-lg)",
                }}
              >
                <button
                  type="button"
                  style={ctxItemStyle}
                  onMouseEnter={(e) => {
                    e.currentTarget.style.background = "var(--accent)";
                  }}
                  onMouseLeave={(e) => {
                    e.currentTarget.style.background = "none";
                  }}
                  onClick={() => {
                    context.close({ restoreFocus: false });
                    if (!isDir) onOpenFile(apiPath);
                  }}
                >
                  Open
                </button>
                <button
                  type="button"
                  style={ctxItemStyle}
                  onMouseEnter={(e) => {
                    e.currentTarget.style.background = "var(--accent)";
                  }}
                  onMouseLeave={(e) => {
                    e.currentTarget.style.background = "none";
                  }}
                  onClick={() => {
                    context.close({ restoreFocus: false });
                    if (isDir) return;
                    const url = `/api/agents/${agentEnc}/sessions/${enc}/workspace/file-content?path=${encodeURIComponent(apiPath)}&raw=true&scope=${scope}`;
                    fetchBlobUrl(url, mimeTypeForPath(apiPath))
                      .then((blobUrl) => {
                        window.open(blobUrl, "_blank");
                        setTimeout(() => URL.revokeObjectURL(blobUrl), 10_000);
                      })
                      .catch(() => window.open(url, "_blank"));
                  }}
                >
                  Open in browser
                </button>
                <a
                  href={
                    isDir
                      ? undefined
                      : `/api/agents/${agentEnc}/sessions/${enc}/workspace/file-content?path=${encodeURIComponent(apiPath)}&raw=true&scope=${scope}`
                  }
                  download={basename(item.path)}
                  style={ctxItemStyle}
                  onMouseEnter={(e) => {
                    (e.currentTarget as HTMLElement).style.background = "var(--accent)";
                  }}
                  onMouseLeave={(e) => {
                    (e.currentTarget as HTMLElement).style.background = "none";
                  }}
                  onClick={() => context.close({ restoreFocus: false })}
                >
                  Download
                </a>
                {canShare && (
                  <button
                    type="button"
                    style={ctxItemStyle}
                    onMouseEnter={(e) => {
                      e.currentTarget.style.background = "var(--accent)";
                    }}
                    onMouseLeave={(e) => {
                      e.currentTarget.style.background = "none";
                    }}
                    onClick={() => {
                      context.close({ restoreFocus: false });
                      setSharePath(apiPath);
                    }}
                  >
                    Share
                  </button>
                )}
                <button
                  type="button"
                  style={ctxItemStyle}
                  onMouseEnter={(e) => {
                    e.currentTarget.style.background = "var(--accent)";
                  }}
                  onMouseLeave={(e) => {
                    e.currentTarget.style.background = "none";
                  }}
                  onClick={() => {
                    context.close({ restoreFocus: false });
                    model.startRenaming(item.path);
                  }}
                >
                  Rename
                </button>
                <button
                  type="button"
                  style={{
                    ...ctxItemStyle,
                    color: "var(--destructive)",
                  }}
                  onMouseEnter={(e) => {
                    e.currentTarget.style.background = "var(--accent)";
                  }}
                  onMouseLeave={(e) => {
                    e.currentTarget.style.background = "none";
                  }}
                  onClick={() => {
                    context.close({ restoreFocus: false });
                    onDelete(apiPath).catch(console.error);
                  }}
                >
                  {t("common.delete")}
                </button>
                <div
                  style={{
                    height: 1,
                    background: "var(--border)",
                    margin: "4px 8px",
                  }}
                />
                <button
                  type="button"
                  style={ctxItemStyle}
                  onMouseEnter={(e) => {
                    e.currentTarget.style.background = "var(--accent)";
                  }}
                  onMouseLeave={(e) => {
                    e.currentTarget.style.background = "none";
                  }}
                  onClick={() => {
                    context.close({ restoreFocus: false });
                    onNewFile();
                  }}
                >
                  New file
                </button>
                <button
                  type="button"
                  style={ctxItemStyle}
                  onMouseEnter={(e) => {
                    e.currentTarget.style.background = "var(--accent)";
                  }}
                  onMouseLeave={(e) => {
                    e.currentTarget.style.background = "none";
                  }}
                  onClick={() => {
                    context.close({ restoreFocus: false });
                    onNewFolder();
                  }}
                >
                  New folder
                </button>
              </div>,
              document.body,
            );
          }}
          onDoubleClick={() => {
            const path = model.getFocusedPath() ?? selectedPaths[0] ?? null;
            if (!path) return;
            if (isDirectoryPath(path)) {
              loadDirectory(path)
                .then(() => {
                  const item = model.getItem(path);
                  if (item?.isDirectory()) (item as { expand: () => void }).expand();
                })
                .catch(console.error);
              return;
            }
            if (loadedPathSet.current.has(path)) {
              onOpenFile(toApi(path));
            }
          }}
        />
      </div>
    </div>
  );
}
