import { useRef, useState } from "react";
import { searchSkills } from "@/lib/api-client/sdk.gen";
import { useI18n } from "@/lib/i18n";
import type { SkillSearchResult } from "@/lib/types";
import type { AgentsPageState } from "./AgentsPage";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Dialog, DialogPopup, DialogTitle, DialogDescription } from "@/components/ui/dialog";
import { Spinner } from "@/components/ui/spinner";

interface Props {
  state: AgentsPageState;
  onClose: () => void;
  onSetScope: (scope: "user" | "agent") => void;
  onInstall: (source: string, scope: "user" | "agent") => Promise<void>;
  onUpload: (file: File, scope: "user" | "agent") => Promise<void>;
  showToast: (msg: string, type?: "success" | "error") => void;
}

export function SkillInstallModal({
  state,
  onClose,
  onSetScope,
  onInstall,
  onUpload,
  showToast,
}: Props) {
  const { t } = useI18n();
  const { skillInstallScope, isAdmin, editingId } = state;
  const [searchQuery, setSearchQuery] = useState("");
  const [searchResults, setSearchResults] = useState<SkillSearchResult[]>([]);
  const [searching, setSearching] = useState(false);
  const searchTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const [installSource, setInstallSource] = useState("");
  const [installing, setInstalling] = useState(false);
  const [uploadFile, setUploadFile] = useState<File | null>(null);
  const [uploading, setUploading] = useState(false);

  const canInstallAgentSkills = isAdmin && !!editingId;

  const doSearch = async (q: string) => {
    if (!q.trim()) {
      setSearchResults([]);
      return;
    }
    setSearching(true);
    try {
      const { data } = await searchSkills({ query: { q, limit: 20 }, throwOnError: true });
      setSearchResults((data as SkillSearchResult[]) ?? []);
    } catch (e) {
      showToast((e as Error).message, "error");
      setSearchResults([]);
    } finally {
      setSearching(false);
    }
  };

  const handleInstall = async (source: string) => {
    setInstalling(true);
    setInstallSource(source);
    try {
      await onInstall(source, skillInstallScope);
    } finally {
      setInstalling(false);
      setInstallSource("");
    }
  };

  const handleUpload = async () => {
    if (!uploadFile) return;
    setUploading(true);
    try {
      await onUpload(uploadFile, skillInstallScope);
    } finally {
      setUploading(false);
    }
  };

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <DialogPopup className="max-w-5xl" showCloseButton={false}>
        <div className="border-b border-border px-6 py-5 flex items-start justify-between gap-4">
          <div className="space-y-1">
            <DialogTitle>Add a skill</DialogTitle>
            <DialogDescription>
              Install from the catalog or upload your own skill bundle.
            </DialogDescription>
          </div>
          <Button onClick={onClose} variant="ghost" size="icon-sm">
            ✕
          </Button>
        </div>
        <div className="px-6 py-4 border-b border-border space-y-2 bg-muted/30">
          <label className="text-xs font-medium text-muted-foreground block uppercase tracking-wide">
            Install target
          </label>
          <div className="flex items-center gap-2 flex-wrap">
            {canInstallAgentSkills && (
              <Button
                onClick={() => onSetScope("agent")}
                variant={skillInstallScope === "agent" ? "default" : "ghost"}
                size="sm"
              >
                Only this agent
              </Button>
            )}
            <Button
              onClick={() => onSetScope("user")}
              variant={skillInstallScope === "user" ? "default" : "ghost"}
              size="sm"
            >
              My profile
            </Button>
          </div>
          {!canInstallAgentSkills && (
            <p className="text-xs text-muted-foreground">
              Agent-only install is available after the agent is saved, and only for admins.
            </p>
          )}
        </div>
        <div className="grid grid-cols-1 xl:grid-cols-[minmax(0,1.3fr)_minmax(320px,0.9fr)] gap-0">
          <section className="p-6 min-w-0 xl:border-r border-border space-y-4">
            <div className="space-y-1">
              <h4 className="font-medium text-base">Browse catalog</h4>
              <p className="text-sm text-muted-foreground">
                Search public skills and install in one click.
              </p>
            </div>
            <Input
              nativeInput
              value={searchQuery}
              onChange={(e) => {
                const q = (e.target as HTMLInputElement).value;
                setSearchQuery(q);
                if (searchTimerRef.current) clearTimeout(searchTimerRef.current);
                searchTimerRef.current = setTimeout(() => doSearch(q), 300);
              }}
              type="text"
              placeholder="Search skills..."
              autoFocus
            />
            {searching && (
              <div className="py-10 text-center flex justify-center">
                <Spinner className="size-6" />
              </div>
            )}
            {!searching && searchResults.length > 0 && (
              <div className="space-y-3 max-h-[28rem] overflow-y-auto pr-1">
                {searchResults.map((s) => (
                  <div
                    key={s.id}
                    className="rounded-xl border border-border bg-background px-4 py-4 space-y-3"
                  >
                    <div className="flex items-start justify-between gap-3">
                      <div className="min-w-0">
                        <div className="flex items-center gap-2 flex-wrap">
                          <p className="font-mono text-sm font-medium truncate">
                            {s.name || s.skillId}
                          </p>
                          <span className="text-xs text-muted-foreground">
                            {s.installs} installs
                          </span>
                        </div>
                        <p className="text-xs text-muted-foreground font-mono truncate mt-1">
                          {s.source}@{s.skillId}
                        </p>
                      </div>
                      <Button
                        onClick={() => handleInstall(`${s.source}@${s.skillId}`)}
                        disabled={installing}
                        loading={installing && installSource === `${s.source}@${s.skillId}`}
                        size="sm"
                      >
                        {t("agents.skills.install")}
                      </Button>
                    </div>
                    {s.description && (
                      <p className="text-sm text-muted-foreground">{s.description}</p>
                    )}
                  </div>
                ))}
              </div>
            )}
            {!searching && searchResults.length === 0 && searchQuery && (
              <div className="rounded-xl border border-dashed border-border p-8 text-center text-sm text-muted-foreground">
                No skills found for that search.
              </div>
            )}
            {!searching && !searchQuery && (
              <div className="rounded-xl border border-dashed border-border p-8 text-center text-sm text-muted-foreground">
                Start typing to search the catalog.
              </div>
            )}
            <div className="space-y-1">
              <h4 className="font-medium text-sm">Or install from source</h4>
              <div className="flex gap-2">
                <Input
                  nativeInput
                  value={installSource}
                  onChange={(e) => setInstallSource((e.target as HTMLInputElement).value)}
                  type="text"
                  placeholder="source@skill-id"
                  size="sm"
                  className="flex-1 font-mono"
                />
                <Button
                  onClick={() => handleInstall(installSource)}
                  disabled={installing || !installSource}
                  loading={installing}
                  size="sm"
                >
                  {t("agents.skills.install")}
                </Button>
              </div>
            </div>
          </section>
          <section className="p-6 space-y-4 bg-muted/20">
            <div className="space-y-1">
              <h4 className="font-medium text-base">Upload zip</h4>
              <p className="text-sm text-muted-foreground">
                Import a skill you already have on disk.
              </p>
            </div>
            <label className="block rounded-xl border-2 border-dashed border-border bg-background px-5 py-8 text-center cursor-pointer hover:border-primary transition-colors">
              <input
                onChange={(e) => setUploadFile(e.target.files?.[0] ?? null)}
                type="file"
                accept=".zip,application/zip"
                className="hidden"
              />
              <div className="space-y-2">
                <div className="text-sm font-medium">
                  {uploadFile ? uploadFile.name : "Choose a .zip file"}
                </div>
                <p className="text-xs text-muted-foreground">
                  Must contain exactly one skill folder with{" "}
                  <span className="font-mono">SKILL.md</span>.
                </p>
                <Button variant="ghost" size="sm">
                  Browse files
                </Button>
              </div>
            </label>
            <ul className="text-xs text-muted-foreground space-y-1 list-disc pl-4">
              <li>
                <span className="font-mono">.zip</span> only
              </li>
              <li>Single skill folder</li>
              <li>
                <span className="font-mono">SKILL.md</span> required
              </li>
            </ul>
            <Button
              onClick={handleUpload}
              disabled={uploading || !uploadFile}
              loading={uploading}
              className="w-full"
            >
              Upload skill
            </Button>
          </section>
        </div>
      </DialogPopup>
    </Dialog>
  );
}
