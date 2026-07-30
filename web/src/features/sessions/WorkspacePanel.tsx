import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import {
  FileTree as FileTreeComponent,
  useFileTree,
  useFileTreeSelection,
} from "@pierre/trees/react";
import { themeToTreeStyles, type TreeThemeInput } from "@pierre/trees";
import { FilePlus, FolderPlus, Trash2, RefreshCw, X, Copy, Eye, EyeOff } from "lucide-react";
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

// A scope rendered as one top-level folder of the unified tree. apiRoot is the
// subdirectory of the scope the folder is rooted at: the project directory for
// the agent scope, the assets area for the shared scope, so managed CLI state
// under the principal root never becomes a file-manager target.
interface TreeRoot {
  scope: Scope;
  label: string;
  apiRoot: string;
  workspace: Workspace | null;
}

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
  const [showHidden, setShowHidden] = useState(false);

  // The shared scope is not tracked by the parent (it owns only the agent
  // workspace and project-dir derivation), so it self-loads here.
  const [sharedWorkspace, setSharedWorkspace] = useState<Workspace | null>(null);
  const [sharedLoading, setSharedLoading] = useState(false);
  // Monotonic token: only the most recent load may write state, so a slow
  // response from a previous session can't clobber the current view.
  const loadToken = useRef(0);
  const loadShared = useCallback(async () => {
    const token = ++loadToken.current;
    setSharedLoading(true);
    try {
      const { data } = await getSessionWorkspace({
        path: { agentId: agentID, sessionId: sessionID },
        query: { show_hidden: true, depth: 2, scope: "user", path: "assets" },
        throwOnError: true,
      });
      if (token === loadToken.current) setSharedWorkspace(data);
    } catch {
      // Also reached when no assets exist yet; the folder renders as empty.
      if (token === loadToken.current) setSharedWorkspace(null);
    } finally {
      if (token === loadToken.current) setSharedLoading(false);
    }
  }, [agentID, sessionID]);
  useEffect(() => {
    if (!sessionID) return;
    // Drop any stale tree before the new session's load lands.
    setSharedWorkspace(null);
    void loadShared();
  }, [sessionID, loadShared]);

  const reloadAll = useCallback(() => {
    onReload(sessionID, projectDir || undefined).catch(console.error);
    void loadShared();
  }, [sessionID, onReload, projectDir, loadShared]);

  const roots = useMemo<TreeRoot[]>(
    () => [
      {
        scope: "agent",
        label: t("sessions.workspace.agentHome"),
        apiRoot: projectDir ?? "",
        workspace,
      },
      {
        scope: "user",
        label: t("sessions.workspace.sharedData"),
        apiRoot: "assets",
        workspace: sharedWorkspace,
      },
    ],
    [t, projectDir, workspace, sharedWorkspace],
  );

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

  // Close the viewer when the file it shows is deleted in the tree.
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

  // One tree, two top-level folders: the agent's private workspace ($HOME) and
  // the shared deliverables area ($STELLA_ASSETS_DIR). The scope split is a
  // backend concept; users get a single file surface and operations route by
  // the folder a node lives under.
  return (
    <UnifiedTree
      agentID={agentID}
      sessionID={sessionID}
      roots={roots}
      loading={workspaceLoading || sharedLoading}
      showHidden={showHidden}
      onToggleHidden={() => setShowHidden((v) => !v)}
      onReload={reloadAll}
      onOpenFile={openFile}
      onDeleted={onDeleted}
    />
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

interface UnifiedTreeProps {
  agentID: string;
  sessionID: string;
  roots: TreeRoot[];
  loading: boolean;
  showHidden: boolean;
  onToggleHidden: () => void;
  onReload: () => void;
  onOpenFile: (path: string, scope: Scope) => void;
  onDeleted: (path: string, scope: Scope) => void;
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

function UnifiedTree({
  agentID,
  sessionID,
  roots,
  loading,
  showHidden,
  onToggleHidden,
  onReload,
  onOpenFile,
  onDeleted,
}: UnifiedTreeProps) {
  const { t } = useI18n();
  const enc = encodeURIComponent(sessionID);
  const agentEnc = encodeURIComponent(agentID);
  const theme = useMemo(() => buildTheme(), []);
  const themeStyles = useMemo(() => themeToTreeStyles(theme), [theme]);
  const [sharePath, setSharePath] = useState<{ path: string; scope: Scope } | null>(null);
  const [newItem, setNewItem] = useState<{ scope: Scope; type: "file" | "dir" } | null>(null);
  const [newItemName, setNewItemName] = useState("");

  // ── Display-path ↔ (scope, API path) mapping ────────────────────────────
  // A display path is "<root label>/<path relative to the root's apiRoot>".
  // The API works in scope-root-relative paths, so the apiRoot prefix is
  // re-attached on the way out and stripped on the way in.
  const rootOf = useCallback(
    (displayPath: string): { root: TreeRoot; rel: string } | null => {
      const clean = displayPath.replace(/\/$/, "");
      const [first, ...rest] = clean.split("/");
      const root = roots.find((r) => r.label === first);
      return root ? { root, rel: rest.join("/") } : null;
    },
    [roots],
  );
  const toApi = useCallback((root: TreeRoot, rel: string): string => {
    if (!rel) return root.apiRoot;
    return root.apiRoot ? `${root.apiRoot}/${rel}` : rel;
  }, []);
  const toDisplay = useCallback((root: TreeRoot, apiPath: string): string => {
    const prefix = root.apiRoot ? `${root.apiRoot}/` : "";
    if (prefix && !apiPath.startsWith(prefix)) return "";
    const rel = prefix ? apiPath.slice(prefix.length) : apiPath;
    return rel ? `${root.label}/${rel}` : "";
  }, []);

  const rootDirs = useMemo(() => roots.map((r) => `${r.label}/`), [roots]);
  const displayPaths = useMemo(() => {
    const paths = [...rootDirs];
    for (const root of roots) {
      for (const p of root.workspace?.paths ?? []) {
        const display = toDisplay(root, p);
        if (!display) continue;
        if (!showHidden && isHiddenPath(display.slice(root.label.length + 1))) continue;
        paths.push(display);
      }
    }
    return paths;
  }, [roots, rootDirs, toDisplay, showHidden]);

  const loadedPathSet = useRef(new Set(displayPaths));
  const loadedDirSet = useRef(new Set(rootDirs.map((d) => d.slice(0, -1))));
  const loadingDirSet = useRef(new Set<string>());

  const createItem = useCallback(async () => {
    const name = newItemName.trim();
    if (!name || !newItem) return;
    const root = roots.find((r) => r.scope === newItem.scope);
    if (!root) return;
    await createWorkspaceFile({
      path: { agentId: agentID, sessionId: sessionID },
      query: { scope: root.scope },
      body: { path: toApi(root, name), is_dir: newItem.type === "dir" },
      throwOnError: true,
    });
    onReload();
    setNewItem(null);
    setNewItemName("");
  }, [agentID, sessionID, roots, newItem, newItemName, toApi, onReload]);

  const deleteItem = useCallback(
    async (displayPath: string) => {
      const parsed = rootOf(displayPath);
      if (!parsed || !parsed.rel) return;
      const apiPath = toApi(parsed.root, parsed.rel);
      if (!confirm(`Delete "${apiPath}"?`)) return;
      await deleteWorkspaceFile({
        path: { agentId: agentID, sessionId: sessionID },
        query: { scope: parsed.root.scope },
        body: { path: apiPath },
        throwOnError: true,
      });
      onReload();
      onDeleted(apiPath, parsed.root.scope);
    },
    [agentID, sessionID, rootOf, toApi, onReload, onDeleted],
  );

  // Reset transient state when the session changes.
  useEffect(() => {
    setNewItem(null);
    setNewItemName("");
    setSharePath(null);
  }, [sessionID]);

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
        const src = rootOf(sourcePath);
        const dst = rootOf(destinationPath);
        // Root folders are fixed, and a rename may not cross scope roots.
        if (!src || !dst || !src.rel || !dst.rel || src.root.scope !== dst.root.scope) {
          model.move(destinationPath, sourcePath);
          return;
        }
        try {
          await moveWorkspaceFile({
            path: { agentId: agentID, sessionId: sessionID },
            query: { scope: src.root.scope },
            body: { path: toApi(src.root, src.rel), new_path: toApi(dst.root, dst.rel) },
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
          for (const srcDisplay of draggedPaths) {
            const src = rootOf(srcDisplay);
            if (!src || !src.rel) continue;
            const destDir = (target.directoryPath ?? "").replace(/\/$/, "");
            const dst = rootOf(destDir);
            // Files cannot move between the private workspace and the shared
            // area from here; that transition changes who can see them.
            if (!dst || src.root.scope !== dst.root.scope) {
              onReload();
              continue;
            }
            const filename = basename(srcDisplay);
            const newRel = dst.rel ? `${dst.rel}/${filename}` : filename;
            await moveWorkspaceFile({
              path: { agentId: agentID, sessionId: sessionID },
              query: { scope: src.root.scope },
              body: { path: toApi(src.root, src.rel), new_path: toApi(dst.root, newRel) },
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
    loadedDirSet.current = new Set(rootDirs.map((d) => d.slice(0, -1)));
    loadingDirSet.current = new Set();
    model.resetPaths(displayPaths, { initialExpandedPaths: rootDirs });
  }, [model, displayPaths, rootDirs]);

  const loadDirectory = useCallback(
    async (displayPath: string) => {
      const dir = displayPath.replace(/\/$/, "");
      if (loadedDirSet.current.has(dir) || loadingDirSet.current.has(dir)) return;
      const parsed = rootOf(dir);
      if (!parsed) return;
      loadingDirSet.current.add(dir);
      try {
        const apiDir = toApi(parsed.root, parsed.rel);
        const { data } = await getSessionWorkspace({
          path: { agentId: agentID, sessionId: sessionID },
          query: {
            show_hidden: true,
            depth: 2,
            scope: parsed.root.scope,
            ...(apiDir ? { path: apiDir } : {}),
          },
          throwOnError: true,
        });
        const added: string[] = [];
        for (const rawPath of data.paths ?? []) {
          const display = toDisplay(parsed.root, rawPath);
          if (!display || loadedPathSet.current.has(display)) continue;
          if (!showHidden && isHiddenPath(display.slice(parsed.root.label.length + 1))) continue;
          loadedPathSet.current.add(display);
          added.push(display);
        }
        for (const nextPath of added) model.add(nextPath);
        loadedDirSet.current.add(dir);
      } catch (e) {
        console.error(e);
      } finally {
        loadingDirSet.current.delete(dir);
      }
    },
    [agentID, sessionID, rootOf, toApi, toDisplay, showHidden, model],
  );

  // Fetch children the moment a not-yet-loaded directory is expanded, however
  // the expansion happened (row click, chevron, keyboard, search auto-expand).
  // The library has no expansion callback, so a generic change subscription
  // scans the known directories; the sets keep each fetch one-shot.
  useEffect(() => {
    return model.subscribe(() => {
      for (const path of loadedPathSet.current) {
        if (!isDirectoryPath(path)) continue;
        const dir = path.slice(0, -1);
        if (loadedDirSet.current.has(dir) || loadingDirSet.current.has(dir)) continue;
        const item = model.getItem(path);
        if (item?.isDirectory() && (item as { isExpanded: () => boolean }).isExpanded())
          void loadDirectory(path);
      }
    });
  }, [model, loadDirectory]);

  // Single click opens a file, like every native file explorer. The tree is a
  // web component, so the row is recovered from the composed event path; clicks
  // on its interactive descendants (context menu, rename input, search) and
  // modified clicks (multi-select) are left to the tree itself. Directory
  // clicks already expand/collapse natively.
  const handleTreeClick = useCallback(
    (e: React.MouseEvent) => {
      if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
      let itemPath: string | null = null;
      for (const entry of e.nativeEvent.composedPath()) {
        if (!(entry instanceof HTMLElement)) continue;
        const ds = entry.dataset;
        if (
          ds.itemActionAffordance !== undefined ||
          ds.itemRenameInput !== undefined ||
          ds.itemFlattenedRenameInput !== undefined ||
          ds.fileTreeSearchContainer !== undefined ||
          ds.fileTreeContextMenuRoot === "true" ||
          ds.type === "context-menu-anchor" ||
          ds.type === "context-menu-trigger"
        ) {
          return;
        }
        if (itemPath === null && ds.itemPath) itemPath = ds.itemPath;
      }
      if (!itemPath) return;
      const item = model.getItem(itemPath);
      if (!item || item.isDirectory()) return;
      const parsed = rootOf(itemPath);
      if (parsed?.rel) onOpenFile(toApi(parsed.root, parsed.rel), parsed.root.scope);
    },
    [model, rootOf, toApi, onOpenFile],
  );

  const selectedPaths = useFileTreeSelection(model);
  const firstSelected = selectedPaths[0] ?? null;
  const selectedParsed = firstSelected ? rootOf(firstSelected) : null;
  // New items land in the selected node's root, or the shared area by default —
  // that is where user-added files (uploads, references) belong.
  const newItemScope: Scope = selectedParsed?.root.scope ?? "user";
  const deletableSelection = Boolean(selectedParsed?.rel);

  return (
    <div className="flex h-full w-full min-w-0 flex-col overflow-hidden bg-sidebar/80">
      <ArtifactShareDialog
        path={sharePath?.path ?? null}
        agentID={agentID}
        sessionID={sessionID}
        scope={sharePath?.scope ?? "user"}
        onClose={() => setSharePath(null)}
      />

      {/* Toolbar */}
      <div className="flex min-h-9 flex-shrink-0 items-center justify-end gap-0 border-b border-border/70 px-2 py-1.5">
        <Button
          variant="ghost"
          size="xs"
          onClick={onToggleHidden}
          title={t(showHidden ? "sessions.workspace.hideHidden" : "sessions.workspace.showHidden")}
          className="px-1 h-6"
        >
          {showHidden ? <Eye className="w-3.5 h-3.5" /> : <EyeOff className="w-3.5 h-3.5" />}
        </Button>
        <Button
          variant="ghost"
          size="xs"
          onClick={() => setNewItem({ scope: newItemScope, type: "file" })}
          title={t("sessions.workspace.newFile")}
          className="px-1 h-6"
        >
          <FilePlus className="w-3.5 h-3.5" />
        </Button>
        <Button
          variant="ghost"
          size="xs"
          onClick={() => setNewItem({ scope: newItemScope, type: "dir" })}
          title={t("sessions.workspace.newFolder")}
          className="px-1 h-6"
        >
          <FolderPlus className="w-3.5 h-3.5" />
        </Button>
        <Button
          variant="ghost"
          size="xs"
          disabled={!deletableSelection}
          onClick={() => firstSelected && deleteItem(firstSelected).catch(console.error)}
          title={t("sessions.workspace.deleteSelected")}
          className={cn(
            "px-1 h-6",
            deletableSelection
              ? "text-destructive hover:bg-destructive/10"
              : "opacity-30 cursor-not-allowed",
          )}
        >
          <Trash2 className="w-3.5 h-3.5" />
        </Button>
        <Button
          variant="ghost"
          size="xs"
          onClick={onReload}
          disabled={loading}
          title={t("sessions.workspace.refresh")}
          className="px-1 h-6"
        >
          <RefreshCw className={cn("w-3.5 h-3.5", loading && "animate-spin")} />
        </Button>
      </div>

      {/* New item form */}
      {newItem !== null && (
        <div className="min-w-0 flex-shrink-0 border-b border-border px-2 py-1.5">
          <form
            onSubmit={(e) => {
              e.preventDefault();
              createItem().catch(console.error);
            }}
            className="flex min-w-0 items-center gap-1.5"
          >
            <span className="min-w-0 shrink-0 truncate text-xs font-mono text-muted-foreground">
              {roots.find((r) => r.scope === newItem.scope)?.label}/
            </span>
            <input
              autoFocus
              value={newItemName}
              onChange={(e) => setNewItemName(e.target.value)}
              onKeyDown={(e) => e.key === "Escape" && (setNewItem(null), setNewItemName(""))}
              className="h-6 min-w-0 flex-1 rounded border border-border bg-background px-1.5 py-0.5 font-mono text-xs focus:border-primary/60 focus:outline-none"
              placeholder={newItem.type === "dir" ? "folder..." : "file..."}
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
                setNewItem(null);
                setNewItemName("");
              }}
              className="text-muted-foreground"
            >
              <X className="w-3 h-3" />
            </Button>
          </form>
        </div>
      )}

      {/* Tree */}
      <div className="min-w-0 flex-1 overflow-hidden">
        <FileTreeComponent
          model={model}
          className="min-w-0 max-w-full overflow-hidden"
          style={{ ...themeStyles, width: "100%", height: "100%" }}
          renderContextMenu={(item, context) => {
            const MENU_W = 160;
            const MENU_H = 240;
            const { anchorRect } = context;
            const parsed = rootOf(item.path);
            if (!parsed) return null;
            const isDir = isDirectoryPath(item.path);
            const isRoot = parsed.rel === "";
            const apiPath = toApi(parsed.root, parsed.rel);
            const scope = parsed.root.scope;
            const canShare = !isDir && isShareableArtifact(apiPath);
            const left =
              anchorRect.right + MENU_W > window.innerWidth
                ? Math.max(0, anchorRect.left - MENU_W)
                : anchorRect.right;
            const top =
              anchorRect.top + MENU_H > window.innerHeight
                ? Math.max(0, anchorRect.bottom - MENU_H)
                : anchorRect.top;
            const hoverable = (extra?: React.CSSProperties) => ({
              style: { ...ctxItemStyle, ...extra },
              onMouseEnter: (e: React.MouseEvent<HTMLElement>) => {
                e.currentTarget.style.background = "var(--accent)";
              },
              onMouseLeave: (e: React.MouseEvent<HTMLElement>) => {
                e.currentTarget.style.background = "none";
              },
            });
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
                {!isRoot && (
                  <>
                    <button
                      type="button"
                      {...hoverable()}
                      onClick={() => {
                        context.close({ restoreFocus: false });
                        if (!isDir) onOpenFile(apiPath, scope);
                      }}
                    >
                      Open
                    </button>
                    <button
                      type="button"
                      {...hoverable()}
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
                      {...hoverable()}
                      onClick={() => context.close({ restoreFocus: false })}
                    >
                      Download
                    </a>
                    {canShare && (
                      <button
                        type="button"
                        {...hoverable()}
                        onClick={() => {
                          context.close({ restoreFocus: false });
                          setSharePath({ path: apiPath, scope });
                        }}
                      >
                        Share
                      </button>
                    )}
                    <button
                      type="button"
                      {...hoverable()}
                      onClick={() => {
                        context.close({ restoreFocus: false });
                        model.startRenaming(item.path);
                      }}
                    >
                      Rename
                    </button>
                    <button
                      type="button"
                      {...hoverable({ color: "var(--destructive)" })}
                      onClick={() => {
                        context.close({ restoreFocus: false });
                        deleteItem(item.path).catch(console.error);
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
                  </>
                )}
                <button
                  type="button"
                  {...hoverable()}
                  onClick={() => {
                    context.close({ restoreFocus: false });
                    setNewItem({ scope, type: "file" });
                  }}
                >
                  New file
                </button>
                <button
                  type="button"
                  {...hoverable()}
                  onClick={() => {
                    context.close({ restoreFocus: false });
                    setNewItem({ scope, type: "dir" });
                  }}
                >
                  New folder
                </button>
              </div>,
              document.body,
            );
          }}
          onClick={handleTreeClick}
        />
      </div>
    </div>
  );
}
