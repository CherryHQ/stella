import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import {
  FileTree as FileTreeComponent,
  useFileTree,
  useFileTreeSelection,
} from "@pierre/trees/react";
import { themeToTreeStyles, type TreeThemeInput } from "@pierre/trees";
import { FilePlus, FolderPlus, Trash2, RefreshCw, X } from "lucide-react";
import { api } from "@/lib/api";
import type { Workspace } from "@/lib/types";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
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

interface ViewerState {
  path: string;
  content: string;
  language: string;
  loading: boolean;
  saving: boolean;
}

export function WorkspacePanel({
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
  const enc = encodeURIComponent(sessionID);

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
        const data = await api<{ content: string; language: string }>(
          "GET",
          `/api/sessions/${enc}/workspace/file-content?path=${encodeURIComponent(path)}`,
        );
        setViewer((v) =>
          v && v.path === path
            ? { ...v, content: data.content ?? "", language: data.language ?? "", loading: false }
            : v,
        );
      } catch (e) {
        console.error(e);
        setViewer(null);
        setMode("tree");
      }
    },
    [enc],
  );

  const saveFile = useCallback(
    async (content: string) => {
      if (!viewer) return;
      setViewer((v) => (v ? { ...v, saving: true } : null));
      try {
        await api("PUT", `/api/sessions/${enc}/workspace/file-content`, {
          path: viewer.path,
          content,
        });
        setViewer((v) => (v ? { ...v, content, saving: false } : null));
      } catch (e) {
        console.error(e);
        setViewer((v) => (v ? { ...v, saving: false } : null));
      }
    },
    [viewer, enc],
  );

  const createItem = useCallback(async () => {
    const name = newItemName.trim();
    if (!name) return;
    const fullPath = projectDir ? `${projectDir}/${name}` : name;
    await api("POST", `/api/sessions/${enc}/workspace/files`, {
      path: fullPath,
      is_dir: newItemType === "dir",
    });
    reload();
    setNewItemType(null);
    setNewItemName("");
  }, [enc, newItemName, newItemType, reload, projectDir]);

  const deleteItem = useCallback(
    async (path: string) => {
      if (!confirm(`Delete "${path}"?`)) return;
      await api("DELETE", `/api/sessions/${enc}/workspace/files`, { path });
      reload();
      if (selectedPath === path) setSelectedPath(null);
      if (viewer?.path === path) {
        setViewer(null);
        setMode("tree");
      }
    },
    [enc, selectedPath, reload, viewer],
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
  const workspaceStats = `${fileCount}F ${dirCount}D ${formatBytes(workspace?.total_bytes ?? 0)}`;

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
        sessionID={sessionID}
        onBack={goBack}
        onSave={saveFile}
      />
    );
  }

  return (
    <div className="flex h-full w-full flex-col overflow-hidden bg-sidebar/80">
      {/* Header */}
      <div className="flex h-12 flex-shrink-0 items-center justify-between border-b border-border/70 pl-8 pr-3">
        <div className="min-w-0 pl-1">
          <span className="block text-[11px] font-semibold tracking-[-0.01em] text-foreground">
            Workspace
          </span>
          <span className="block truncate font-mono text-[10px] text-muted-foreground">
            {workspaceStats}
          </span>
        </div>
        <div className="flex items-center gap-0">
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
          <div className="w-px h-3 bg-border mx-0.5" />
          {workspaceLoading ? (
            <div className="w-3 h-3 border border-muted-foreground/30 border-t-muted-foreground rounded-full animate-spin mx-1" />
          ) : (
            <span className="text-[10px] font-mono text-muted-foreground/30 mx-1">
              <span
                title={
                  workspace?.root
                    ? `${workspace.root}\n${fileCount} files, ${dirCount} dirs, ${formatBytes(workspace.total_bytes)}`
                    : undefined
                }
              >
                {workspaceStats}
              </span>
            </span>
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

interface TreeWithSearchProps {
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
  const theme = useMemo(() => buildTheme(), []);
  const themeStyles = useMemo(() => themeToTreeStyles(theme), [theme]);

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
          await api<Workspace>("PATCH", `/api/sessions/${enc}/workspace/files`, {
            path: toApi(sourcePath),
            new_path: toApi(destinationPath),
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
            await api<Workspace>("PATCH", `/api/sessions/${enc}/workspace/files`, {
              path: toApi(src),
              new_path: toApi(newPath),
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
        const data = await api<Workspace>(
          "GET",
          `/api/sessions/${enc}/workspace?show_hidden=true&depth=2&path=${encodeURIComponent(apiDir)}`,
        );
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
                    const url = `/api/sessions/${enc}/workspace/file-content?path=${encodeURIComponent(apiPath)}&raw=true`;
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
                      : `/api/sessions/${enc}/workspace/file-content?path=${encodeURIComponent(apiPath)}&raw=true`
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
