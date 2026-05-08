import { useCallback, useEffect, useMemo, useState } from "react";
import { FileTree as FileTreeComponent } from "@pierre/trees/react";
import { useFileTree } from "@pierre/trees/react";
import { useFileTreeSelection } from "@pierre/trees/react";
import { themeToTreeStyles, type TreeThemeInput } from "@pierre/trees";
import { api } from "@/lib/api";
import type { Workspace } from "@/lib/types";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";

interface Props {
  sessionID: string;
  workspace: Workspace | null;
  workspaceLoading: boolean;
  onWorkspaceChange: (w: Workspace) => void;
  onOpenFile: (path: string) => void;
}

function buildTheme(): TreeThemeInput {
  const style = getComputedStyle(document.documentElement);
  const resolve = (v: string): string | undefined => {
    const val = style.getPropertyValue(v).trim();
    return val || undefined;
  };
  const isDark =
    document.documentElement.dataset.theme === "dark" ||
    window.matchMedia("(prefers-color-scheme: dark)").matches;

  const colorEntries: Array<[string, string | undefined]> = [
    ["input.background", resolve("--color-base-200")],
    ["input.border", resolve("--color-base-300")],
    ["sideBar.background", resolve("--color-base-100")],
    ["sideBar.foreground", resolve("--color-base-content")],
    ["sideBar.border", resolve("--color-base-300")],
    ["list.hoverBackground", resolve("--color-base-200")],
    ["list.activeSelectionBackground", resolve("--color-base-200")],
    ["list.activeSelectionForeground", resolve("--color-primary")],
  ];
  const colors: Record<string, string> = {};
  for (const [k, v] of colorEntries) {
    if (v) colors[k] = v;
  }

  return {
    type: isDark ? "dark" : "light",
    bg: resolve("--color-base-100"),
    fg: resolve("--color-base-content"),
    colors,
  };
}

interface FileTreePanelProps {
  sessionID: string;
  workspace: Workspace;
  onWorkspaceChange: (w: Workspace) => void;
  onOpenFile: (path: string) => void;
  onSelectedPath: (path: string | null) => void;
  onContextMenu: (e: React.MouseEvent, path: string | null) => void;
}

function FileTreePanel({
  sessionID,
  workspace,
  onWorkspaceChange,
  onOpenFile,
  onSelectedPath,
  onContextMenu,
}: FileTreePanelProps) {
  const enc = encodeURIComponent(sessionID);
  const theme = useMemo(() => buildTheme(), []);
  const themeStyles = useMemo(() => themeToTreeStyles(theme), [theme]);

  const { model } = useFileTree({
    paths: workspace.paths ?? [],
    initialExpansion: 1,
    search: true,
    renaming: {
      onRename: async ({ sourcePath, destinationPath }) => {
        try {
          await api<Workspace>("PATCH", `/api/sessions/${enc}/workspace/files`, {
            path: sourcePath,
            new_path: destinationPath,
          });
        } catch (e: unknown) {
          const msg = e instanceof Error ? e.message : "Rename failed";
          console.error("rename:", msg);
          model.move(destinationPath, sourcePath);
        }
      },
      onError: (message: string) => {
        console.error("rename error:", message);
      },
    },
    dragAndDrop: {
      onDropComplete: async ({ draggedPaths, target }) => {
        try {
          for (const src of draggedPaths) {
            const filename = src.split("/").pop()!;
            const destDir = target.directoryPath ?? "";
            const newPath = destDir ? `${destDir}/${filename}` : filename;
            await api<Workspace>("PATCH", `/api/sessions/${enc}/workspace/files`, {
              path: src,
              new_path: newPath,
            });
          }
        } catch (e: unknown) {
          const msg = e instanceof Error ? e.message : "Move failed";
          console.error("drop:", msg);
          // Reload workspace to revert the optimistic tree update
          const data = await api<Workspace>("GET", `/api/sessions/${enc}/workspace`);
          onWorkspaceChange(data);
        }
      },
      onDropError: (message: string) => {
        console.error("drop error:", message);
      },
    },
  });

  const selectedPaths = useFileTreeSelection(model);

  // Propagate selection to parent for delete button enable/disable
  const firstSelected = selectedPaths[0] ?? null;
  useEffect(() => {
    onSelectedPath(firstSelected);
  }, [firstSelected, onSelectedPath]);

  return (
    <FileTreeComponent
      model={model}
      style={{ ...themeStyles, width: "100%", height: "100%" }}
      onContextMenu={(e) => {
        e.preventDefault();
        const path = model.getFocusedPath() ?? selectedPaths[0] ?? null;
        onContextMenu(e as unknown as React.MouseEvent, path);
      }}
      onDoubleClick={() => {
        const path = model.getFocusedPath() ?? selectedPaths[0] ?? null;
        if (path && workspace.paths?.includes(path)) {
          onOpenFile(path);
        }
      }}
    />
  );
}

export function WorkspacePanel({
  sessionID,
  workspace,
  workspaceLoading,
  onWorkspaceChange,
  onOpenFile,
}: Props) {
  const [newItemType, setNewItemType] = useState<"file" | "dir" | null>(null);
  const [newItemName, setNewItemName] = useState("");
  const [selectedPath, setSelectedPath] = useState<string | null>(null);
  const [ctxMenu, setCtxMenu] = useState<{
    show: boolean;
    x: number;
    y: number;
    path: string | null;
  }>({ show: false, x: 0, y: 0, path: null });

  const enc = encodeURIComponent(sessionID);

  const createItem = useCallback(async () => {
    const name = newItemName.trim();
    if (!name) return;
    const isDir = newItemType === "dir";
    const data = await api<Workspace>("POST", `/api/sessions/${enc}/workspace/files`, {
      path: name,
      is_dir: isDir,
    });
    onWorkspaceChange(data);
    setNewItemType(null);
    setNewItemName("");
  }, [enc, newItemName, newItemType, onWorkspaceChange]);

  const deleteItem = useCallback(
    async (path: string) => {
      if (!confirm(`Delete "${path}"?`)) return;
      const data = await api<Workspace>("DELETE", `/api/sessions/${enc}/workspace/files`, { path });
      onWorkspaceChange(data);
      if (selectedPath === path) setSelectedPath(null);
    },
    [enc, selectedPath, onWorkspaceChange],
  );

  const handleContextMenu = useCallback((e: React.MouseEvent, path: string | null) => {
    setCtxMenu({ show: true, x: e.clientX, y: e.clientY, path });
  }, []);

  const fileCount = workspace?.paths?.length ?? 0;

  return (
    <div className="w-full flex flex-col overflow-hidden bg-background h-full">
      {/* Header */}
      <div className="flex items-center justify-between px-3 pt-2.5 pb-2 border-b border-border flex-shrink-0">
        <span className="text-[9px] font-mono text-muted-foreground/40 uppercase tracking-wider">
          Workspace
        </span>
        <div className="flex items-center gap-0.5">
          <Button
            variant="ghost"
            size="xs"
            onClick={() => setNewItemType("file")}
            title="New File"
            className="px-1"
          >
            <svg
              className="w-3.5 h-3.5"
              fill="none"
              viewBox="0 0 24 24"
              strokeWidth="1.8"
              stroke="currentColor"
            >
              <path
                strokeLinecap="round"
                strokeLinejoin="round"
                d="M19.5 14.25v-2.625a3.375 3.375 0 0 0-3.375-3.375h-1.5A1.125 1.125 0 0 1 13.5 7.125v-1.5a3.375 3.375 0 0 0-3.375-3.375H8.25m3.75 9v6m3-3H9m1.5-12H5.625c-.621 0-1.125.504-1.125 1.125v17.25c0 .621.504 1.125 1.125 1.125h12.75c.621 0 1.125-.504 1.125-1.125V11.25a9 9 0 0 0-9-9Z"
              />
            </svg>
          </Button>
          <Button
            variant="ghost"
            size="xs"
            onClick={() => setNewItemType("dir")}
            title="New Folder"
            className="px-1"
          >
            <svg
              className="w-3.5 h-3.5"
              fill="none"
              viewBox="0 0 24 24"
              strokeWidth="1.8"
              stroke="currentColor"
            >
              <path
                strokeLinecap="round"
                strokeLinejoin="round"
                d="M12 10.5v6m3-3H9m4.06-7.19-2.12-2.12a1.5 1.5 0 0 0-1.061-.44H4.5A2.25 2.25 0 0 0 2.25 6v12a2.25 2.25 0 0 0 2.25 2.25h15A2.25 2.25 0 0 0 21.75 18V9a2.25 2.25 0 0 0-2.25-2.25h-5.379a1.5 1.5 0 0 1-1.06-.44Z"
              />
            </svg>
          </Button>
          <Button
            variant="ghost"
            size="xs"
            disabled={!selectedPath}
            onClick={() => selectedPath && deleteItem(selectedPath).catch(console.error)}
            title="Delete selected"
            className={cn(
              "px-1",
              selectedPath
                ? "text-destructive hover:bg-destructive/10"
                : "opacity-30 cursor-not-allowed",
            )}
          >
            <svg
              className="w-3.5 h-3.5"
              fill="none"
              viewBox="0 0 24 24"
              strokeWidth="1.8"
              stroke="currentColor"
            >
              <path
                strokeLinecap="round"
                strokeLinejoin="round"
                d="m14.74 9-.346 9m-4.788 0L9.26 9m9.968-3.21c.342.052.682.107 1.022.166m-1.022-.165L18.16 19.673a2.25 2.25 0 0 1-2.244 2.077H8.084a2.25 2.25 0 0 1-2.244-2.077L4.772 5.79m14.456 0a48.108 48.108 0 0 0-3.478-.397m-12 .562c.34-.059.68-.114 1.022-.165m0 0a48.11 48.11 0 0 1 3.478-.397m7.5 0v-.916c0-1.18-.91-2.164-2.09-2.201a51.964 51.964 0 0 0-3.32 0c-1.18.037-2.09 1.022-2.09 2.201v.916m7.5 0a48.667 48.667 0 0 0-7.5 0"
              />
            </svg>
          </Button>
          <div className="w-px h-3 bg-border mx-0.5" />
          {workspaceLoading && (
            <div className="w-3 h-3 border border-muted-foreground/30 border-t-muted-foreground rounded-full animate-spin" />
          )}
          {!workspaceLoading && workspace && (
            <span className="text-[10px] font-mono text-muted-foreground/30">
              {fileCount >= 1000 ? "1000+" : fileCount} files
            </span>
          )}
        </div>
      </div>

      {/* New item form */}
      {newItemType !== null && (
        <div className="px-3 py-2 border-b border-border flex-shrink-0 bg-muted/50">
          <form
            onSubmit={(e) => {
              e.preventDefault();
              createItem().catch(console.error);
            }}
            className="flex items-center gap-1.5"
          >
            <span className="text-[10px] font-mono text-muted-foreground/50">
              {newItemType === "dir" ? "folder:" : "file:"}
            </span>
            <input
              autoFocus
              value={newItemName}
              onChange={(e) => setNewItemName(e.target.value)}
              onKeyDown={(e) => e.key === "Escape" && (setNewItemType(null), setNewItemName(""))}
              className="flex-1 text-xs font-mono border border-border rounded px-2 py-1 bg-background h-6"
              placeholder="name..."
              autoComplete="off"
            />
            <Button type="submit" size="xs" className="h-6 min-h-0">
              Add
            </Button>
            <Button
              type="button"
              variant="ghost"
              size="xs"
              className="h-6 min-h-0"
              onClick={() => {
                setNewItemType(null);
                setNewItemName("");
              }}
            >
              ✕
            </Button>
          </form>
        </div>
      )}

      {/* File tree / empty state */}
      {!workspaceLoading && workspace && (workspace.paths?.length ?? 0) === 0 && !newItemType && (
        <div className="px-4 py-3 text-[10px] font-mono text-muted-foreground/40">
          Workspace is empty. Use the buttons above to create files.
        </div>
      )}

      {!workspaceLoading && workspace && (workspace.paths?.length ?? 0) > 0 && (
        <div className="flex-1 overflow-hidden">
          <FileTreePanel
            sessionID={sessionID}
            workspace={workspace}
            onWorkspaceChange={onWorkspaceChange}
            onOpenFile={onOpenFile}
            onSelectedPath={setSelectedPath}
            onContextMenu={handleContextMenu}
          />
        </div>
      )}

      {/* Context menu */}
      {ctxMenu.show && (
        <div
          className="fixed bg-background border border-border rounded-lg shadow-lg py-1 min-w-36 text-sm z-[9999]"
          style={{ left: ctxMenu.x, top: ctxMenu.y }}
          onClick={() => setCtxMenu((m) => ({ ...m, show: false }))}
          onMouseLeave={() => setCtxMenu((m) => ({ ...m, show: false }))}
        >
          {ctxMenu.path && (
            <>
              <button
                onClick={() => {
                  if (ctxMenu.path) onOpenFile(ctxMenu.path);
                }}
                className="flex items-center gap-2 w-full px-3 py-1.5 text-left hover:bg-muted text-xs font-mono"
              >
                Open
              </button>
              <button
                onClick={() => {
                  if (ctxMenu.path) deleteItem(ctxMenu.path).catch(console.error);
                }}
                className="flex items-center gap-2 w-full px-3 py-1.5 text-left hover:bg-destructive/10 text-destructive text-xs font-mono"
              >
                Delete
              </button>
              <div className="border-t border-border my-1" />
            </>
          )}
          <button
            onClick={() => setNewItemType("file")}
            className="flex items-center gap-2 w-full px-3 py-1.5 text-left hover:bg-muted text-xs font-mono"
          >
            New File
          </button>
          <button
            onClick={() => setNewItemType("dir")}
            className="flex items-center gap-2 w-full px-3 py-1.5 text-left hover:bg-muted text-xs font-mono"
          >
            New Folder
          </button>
        </div>
      )}
    </div>
  );
}
