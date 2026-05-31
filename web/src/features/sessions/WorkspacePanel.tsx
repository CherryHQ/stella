import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import {
  FileTree as FileTreeComponent,
  useFileTree,
  useFileTreeSelection,
} from "@pierre/trees/react";
import { themeToTreeStyles, type TreeThemeInput } from "@pierre/trees";
import { FilePlus, FolderPlus, Trash2, RefreshCw, X, Copy } from "lucide-react";
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

function isShareableArtifact(path: string): boolean {
  return /\.(html?|mdx?|markdown|png|jpe?g|gif|webp|svg|avif|bmp|ico|pdf)$/i.test(path);
}

interface ViewerState {
  path: string;
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
  const [newItemType, setNewItemType] = useState<"file" | "dir" | null>(null);
  const [newItemName, setNewItemName] = useState("");
  const [selectedPath, setSelectedPath] = useState<string | null>(null);
  const [rootCopied, setRootCopied] = useState(false);
  const reload = useCallback(() => {
    onReload(sessionID, projectDir || undefined).catch(console.error);
  }, [sessionID, onReload, projectDir]);

  // File operations
  const openFile = useCallback(
    async (path: string) => {
      setViewer({ path, content: "", language: "", loading: true, saving: false });
      setMode("viewer");
      if (isNonTextFile(path)) {
        setViewer((v) => (v ? { ...v, loading: false } : null));
        return;
      }
      try {
        const { data } = await getWorkspaceFileContent({
          path: { agentId: agentID, sessionId: sessionID },
          query: { path },
          throwOnError: true,
        });
        const file = data as { content?: string; language?: string };
        setViewer((v) =>
          v && v.path === path
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

  const createItem = useCallback(async () => {
    const name = newItemName.trim();
    if (!name) return;
    const fullPath = projectDir ? `${projectDir}/${name}` : name;
    await createWorkspaceFile({
      path: { agentId: agentID, sessionId: sessionID },
      body: { path: fullPath, is_dir: newItemType === "dir" },
      throwOnError: true,
    });
    reload();
    setNewItemType(null);
    setNewItemName("");
  }, [agentID, sessionID, newItemName, newItemType, reload, projectDir]);

  const deleteItem = useCallback(
    async (path: string) => {
      if (!confirm(`Delete "${path}"?`)) return;
      await deleteWorkspaceFile({
        path: { agentId: agentID, sessionId: sessionID },
        body: { path },
        throwOnError: true,
      });
      reload();
      if (selectedPath === path) setSelectedPath(null);
      if (viewer?.path === path) {
        setViewer(null);
        setMode("tree");
      }
    },
    [agentID, sessionID, selectedPath, reload, viewer],
  );

  const goBack = useCallback(() => {
    setMode("tree");
    setViewer(null);
  }, []);

  // Reset mode when session changes
  useEffect(() => {
    setMode("tree");
    setViewer(null);
    setSelectedPath(null);
  }, [sessionID]);

  const entryCount = workspace?.paths?.length ?? 0;
  const fileCount = workspace?.total_files ?? 0;
  const dirCount = workspace?.total_dirs ?? 0;
  const rootLabel = workspace?.root ?? "";
  const workspaceStats = `${fileCount.toLocaleString()} files · ${dirCount.toLocaleString()} folders · ${formatBytes(workspace?.total_bytes ?? 0)}`;
  const copyRootPath = useCallback(() => {
    if (!workspace?.root) return;
    navigator.clipboard
      .writeText(workspace.root)
      .then(() => {
        setRootCopied(true);
        window.setTimeout(() => setRootCopied(false), 1400);
      })
      .catch(console.error);
  }, [workspace?.root]);

  if (!sessionID) {
    return (
      <div className="flex h-full w-full flex-col overflow-hidden bg-sidebar/80">
        <div className="flex h-12 flex-shrink-0 items-center justify-between border-b border-border/70 px-4">
          <span className="text-[9px] font-mono text-muted-foreground/40 uppercase tracking-wider">
            {t("sessions.workspace.title")}
          </span>
        </div>
        <div className="flex-1 flex items-center justify-center">
          <p className="text-xs text-muted-foreground/50 font-mono">
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
        onBack={goBack}
        onSave={saveFile}
      />
    );
  }

  return (
    <div className="flex h-full w-full flex-col overflow-hidden bg-sidebar/80">
      {/* Header */}
      <div className="flex min-h-12 flex-shrink-0 items-center justify-between gap-3 border-b border-border/70 py-1.5 pl-8 pr-3">
        <div className="min-w-0 flex-1 pl-1">
          <span
            className="block truncate font-mono text-[10px] font-medium text-muted-foreground"
            title={
              workspace?.root
                ? `${workspace.root}\n${fileCount.toLocaleString()} files, ${dirCount.toLocaleString()} folders, ${formatBytes(workspace.total_bytes)}`
                : undefined
            }
          >
            {workspaceStats}
          </span>
          {rootLabel && (
            <div className="flex min-w-0 items-center gap-1">
              <button
                type="button"
                onClick={copyRootPath}
                className="block min-w-0 truncate rounded-sm font-mono text-[10px] text-muted-foreground/55 transition-colors hover:text-foreground"
                title={workspace?.root}
              >
                in {rootLabel}
              </button>
              {rootCopied && (
                <span className="shrink-0 rounded bg-muted px-1 py-0.5 text-[9px] font-medium text-muted-foreground">
                  Copied
                </span>
              )}
            </div>
          )}
        </div>
        <div className="flex shrink-0 items-center gap-0">
          <Button
            variant="ghost"
            size="xs"
            onClick={() => setNewItemType("file")}
            title="New file"
            className="px-1 h-6"
          >
            <FilePlus className="w-3.5 h-3.5" />
          </Button>
          <Button
            variant="ghost"
            size="xs"
            onClick={() => setNewItemType("dir")}
            title="New folder"
            className="px-1 h-6"
          >
            <FolderPlus className="w-3.5 h-3.5" />
          </Button>
          <Button
            variant="ghost"
            size="xs"
            disabled={!selectedPath}
            onClick={() => selectedPath && deleteItem(selectedPath).catch(console.error)}
            title="Delete selected"
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
            disabled={workspaceLoading}
            title="Refresh"
            className="px-1 h-6"
          >
            <RefreshCw className={cn("w-3.5 h-3.5", workspaceLoading && "animate-spin")} />
          </Button>
          {workspaceLoading && (
            <div className="w-3 h-3 border border-muted-foreground/30 border-t-muted-foreground rounded-full animate-spin mx-1" />
          )}
        </div>
      </div>

      {/* New item form */}
      {newItemType !== null && (
        <div className="px-2 py-1.5 border-b border-border flex-shrink-0 bg-muted/30">
          <form
            onSubmit={(e) => {
              e.preventDefault();
              createItem().catch(console.error);
            }}
            className="flex items-center gap-1.5"
          >
            <span className="text-[10px] font-mono text-muted-foreground/50">
              {newItemType === "dir" ? "dir" : "file"}:
            </span>
            <input
              autoFocus
              value={newItemName}
              onChange={(e) => setNewItemName(e.target.value)}
              onKeyDown={(e) => e.key === "Escape" && (setNewItemType(null), setNewItemName(""))}
              className="flex-1 text-[11px] font-mono border border-border rounded px-1.5 py-0.5 bg-background focus:outline-none focus:border-primary/60 h-6"
              placeholder="name..."
              autoComplete="off"
            />
            <Button type="submit" size="xs" className="h-6 min-h-0 text-[11px]">
              {t("common.add")}
            </Button>
            <button
              type="button"
              onClick={() => {
                setNewItemType(null);
                setNewItemName("");
              }}
              className="p-0.5 rounded hover:bg-muted text-muted-foreground"
            >
              <X className="w-3 h-3" />
            </button>
          </form>
        </div>
      )}

      {/* Empty state */}
      {!workspaceLoading && workspace && entryCount === 0 && !newItemType && (
        <div className="px-4 py-8 text-center">
          <p className="text-[11px] font-mono text-muted-foreground/40">Empty workspace</p>
        </div>
      )}

      {/* File tree */}
      {!workspaceLoading && workspace && entryCount > 0 && (
        <div className="flex-1 overflow-hidden">
          <TreeWithSearch
            agentID={agentID}
            sessionID={sessionID}
            workspace={workspace}
            onReload={reload}
            onOpenFile={openFile}
            onSelectedPath={setSelectedPath}
            onDelete={deleteItem}
            onNewFile={() => setNewItemType("file")}
            onNewFolder={() => setNewItemType("dir")}
            projectDir={projectDir}
          />
        </div>
      )}

      {/* Loading state for tree */}
      {workspaceLoading && !workspace && (
        <div className="flex-1 flex items-center justify-center">
          <div className="w-4 h-4 border border-muted-foreground/30 border-t-muted-foreground rounded-full animate-spin" />
        </div>
      )}
    </div>
  );
}

interface ArtifactShareDialogProps {
  path: string | null;
  agentID: string;
  sessionID: string;
  onClose: () => void;
}

const expirationOptions = [
  { value: "1h", label: "1 hour" },
  { value: "1d", label: "1 day" },
  { value: "7d", label: "7 days" },
  { value: "never", label: "Never" },
];

function ArtifactShareDialog({ path, agentID, sessionID, onClose }: ArtifactShareDialogProps) {
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
          expires_in: expiresIn as "1h" | "1d" | "7d" | "never",
        },
        throwOnError: true,
      });
      setShare({ id: result.id, url: result.url });
      await navigator.clipboard?.writeText(result.url).catch(() => undefined);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed to share artifact");
    } finally {
      setCreating(false);
    }
  }, [agentID, expiresIn, path, sessionID]);

  const revokeShare = useCallback(async () => {
    if (!share) return;
    setRevoking(true);
    setError(null);
    try {
      await sdkRevokeShare({ path: { id: share.id }, throwOnError: true });
      setShare(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed to revoke share");
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
          <DialogTitle>Share artifact</DialogTitle>
          <DialogDescription>
            Create a public, read-only snapshot link for {path ? basename(path) : "this file"}.
          </DialogDescription>
        </DialogHeader>
        <DialogPanel className="space-y-4">
          <div className="space-y-2">
            <label
              className="text-xs font-medium text-muted-foreground"
              htmlFor="artifact-share-expiry"
            >
              Expiration
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
              <div className="mb-2 text-xs font-medium text-muted-foreground">Public URL</div>
              <div className="flex items-center gap-2">
                <input
                  readOnly
                  className="min-w-0 flex-1 rounded-md border bg-background px-2 py-1.5 text-xs font-mono"
                  value={share.url}
                />
                <Button size="sm" variant="outline" onClick={copyShare}>
                  <Copy className="size-3.5" />
                  Copy
                </Button>
              </div>
            </div>
          )}
          {error && <p className="text-sm text-destructive">{error}</p>}
        </DialogPanel>
        <DialogFooter>
          {share ? (
            <Button variant="destructive" disabled={revoking} onClick={revokeShare}>
              {revoking ? "Revoking…" : "Revoke link"}
            </Button>
          ) : (
            <Button disabled={creating} onClick={createShare}>
              {creating ? "Creating…" : "Create link"}
            </Button>
          )}
          <Button variant="ghost" onClick={onClose}>
            Close
          </Button>
        </DialogFooter>
      </DialogPopup>
    </Dialog>
  );
}

interface TreeWithSearchProps {
  agentID: string;
  sessionID: string;
  workspace: Workspace;
  onReload: () => void;
  onOpenFile: (path: string) => void;
  onSelectedPath: (path: string | null) => void;
  onDelete: (path: string) => Promise<void>;
  onNewFile: () => void;
  onNewFolder: () => void;
  projectDir?: string;
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
  workspace,
  onReload,
  onOpenFile,
  onSelectedPath,
  onDelete,
  onNewFile,
  onNewFolder,
  projectDir,
}: TreeWithSearchProps) {
  const { t } = useI18n();
  const enc = encodeURIComponent(sessionID);
  const agentEnc = encodeURIComponent(agentID);
  const theme = useMemo(() => buildTheme(), []);
  const themeStyles = useMemo(() => themeToTreeStyles(theme), [theme]);
  const [sharePath, setSharePath] = useState<string | null>(null);

  const prefix = projectDir ? projectDir + "/" : "";
  const toDisplay = useCallback(
    (p: string) => (prefix && p.startsWith(prefix) ? p.slice(prefix.length) : p),
    [prefix],
  );
  const toApi = useCallback((p: string) => (prefix ? prefix + p : p), [prefix]);

  const displayPaths = useMemo(
    () => (workspace.paths ?? []).map((p) => toDisplay(p)).filter(Boolean),
    [workspace.paths, toDisplay],
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
          query: { show_hidden: true, depth: 2, path: apiDir },
          throwOnError: true,
        });
        const added: string[] = [];
        for (const rawPath of data.paths ?? []) {
          const displayPath = toDisplay(rawPath);
          if (!displayPath || loadedPathSet.current.has(displayPath)) continue;
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
    [enc, model, prefix],
  );

  const selectedPaths = useFileTreeSelection(model);

  const firstSelected = selectedPaths[0] ?? null;
  const firstSelectedApi = firstSelected ? toApi(firstSelected) : null;
  useEffect(() => {
    onSelectedPath(firstSelectedApi);
  }, [firstSelectedApi, onSelectedPath]);

  return (
    <div className="flex flex-col h-full">
      <ArtifactShareDialog
        path={sharePath}
        agentID={agentID}
        sessionID={sessionID}
        onClose={() => setSharePath(null)}
      />
      <div className="flex-1 overflow-hidden">
        <FileTreeComponent
          model={model}
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
                  boxShadow: "0 4px 12px rgba(0,0,0,.15)",
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
                    const url = `/api/agents/${agentEnc}/sessions/${enc}/workspace/file-content?path=${encodeURIComponent(apiPath)}&raw=true`;
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
                      : `/api/agents/${agentEnc}/sessions/${enc}/workspace/file-content?path=${encodeURIComponent(apiPath)}&raw=true`
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
