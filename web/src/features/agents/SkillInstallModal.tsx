import { useRef, useState } from "react";
import { searchSkills } from "@/lib/api-client/sdk.gen";
import { useI18n } from "@/lib/i18n";
import type { SkillSearchResult } from "@/lib/types";
import type { AgentsPageState } from "./agent-detail-state";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Dialog, DialogPopup, DialogTitle, DialogDescription } from "@/components/ui/dialog";
import { Spinner } from "@/components/ui/spinner";

interface Props {
  state: AgentsPageState;
  onClose: () => void;
  onSetScope: (scope: "user_agent" | "system_agent") => void;
  onInstall: (source: string, scope: "user_agent" | "system_agent") => Promise<void>;
  onUpload: (file: File, scope: "user_agent" | "system_agent") => Promise<void>;
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
      setSearchResults((data?.skills as SkillSearchResult[]) ?? []);
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
            <DialogTitle>{t("agents.skills.addSkill")}</DialogTitle>
            <DialogDescription>{t("agents.skills.addSkillDesc")}</DialogDescription>
          </div>
          <Button onClick={onClose} variant="ghost" size="icon-sm" className="cursor-pointer">
            ✕
          </Button>
        </div>
        <div className="px-6 py-4 border-b border-border space-y-2.5">
          <label className="text-xs font-semibold text-muted-foreground block">
            {t("agents.skills.installTarget")}
          </label>
          <div className="flex items-center border border-border p-0.5 rounded-lg w-fit">
            {canInstallAgentSkills && (
              <button
                type="button"
                onClick={() => onSetScope("system_agent")}
                className={`px-3 py-1.5 text-xs font-medium rounded-md cursor-pointer ${
                  skillInstallScope === "system_agent"
                    ? "bg-card text-foreground border border-border/10"
                    : "text-muted-foreground hover:text-foreground"
                }`}
              >
                {t("agents.skills.onlyThisAgent")}
              </button>
            )}
            <button
              type="button"
              onClick={() => onSetScope("user_agent")}
              className={`px-3 py-1.5 text-xs font-medium rounded-md cursor-pointer ${
                skillInstallScope === "user_agent"
                  ? "bg-card text-foreground border border-border/10"
                  : "text-muted-foreground hover:text-foreground"
              }`}
            >
              {t("agents.skills.myProfile")}
            </button>
          </div>
          {!canInstallAgentSkills && (
            <p className="text-xs text-muted-foreground">{t("agents.skills.agentOnlyHint")}</p>
          )}
        </div>
        <div className="grid grid-cols-1 xl:grid-cols-[minmax(0,1.3fr)_minmax(320px,0.9fr)] gap-0">
          <section className="p-6 min-w-0 xl:border-r border-border space-y-5">
            <div className="space-y-1">
              <h4 className="font-semibold text-sm text-foreground">
                {t("agents.skills.browseCatalog")}
              </h4>
              <p className="text-xs text-muted-foreground">
                {t("agents.skills.browseCatalogDesc")}
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
              placeholder={t("agents.skills.searchPlaceholder")}
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
                    className="rounded-xl border border-border bg-card p-4 space-y-3 transition-colors hover:border-foreground/10"
                  >
                    <div className="flex items-start justify-between gap-3">
                      <div className="min-w-0">
                        <div className="flex items-center gap-2 flex-wrap">
                          <p className="font-mono text-sm font-medium truncate text-foreground">
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
                        className="cursor-pointer"
                      >
                        {t("agents.skills.install")}
                      </Button>
                    </div>
                    {s.description && (
                      <p className="text-xs text-muted-foreground leading-relaxed">
                        {s.description}
                      </p>
                    )}
                  </div>
                ))}
              </div>
            )}
            {!searching && searchResults.length === 0 && searchQuery && (
              <div className="rounded-xl border border-dashed border-border p-8 text-center text-xs text-muted-foreground">
                {t("agents.skills.noSkillsFound")}
              </div>
            )}
            {!searching && !searchQuery && (
              <div className="rounded-xl border border-dashed border-border p-8 text-center text-xs text-muted-foreground">
                {t("agents.skills.startTyping")}
              </div>
            )}
            <div className="space-y-2 pt-2 border-t border-border/10">
              <h4 className="font-semibold text-xs text-muted-foreground">
                {t("agents.skills.installFromSource")}
              </h4>
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
                  className="cursor-pointer"
                >
                  {t("agents.skills.install")}
                </Button>
              </div>
            </div>
          </section>
          <section className="p-6 space-y-5">
            <div className="space-y-1">
              <h4 className="font-semibold text-sm text-foreground">
                {t("agents.skills.uploadZip")}
              </h4>
              <p className="text-xs text-muted-foreground">{t("agents.skills.uploadDesc")}</p>
            </div>
            <label className="block rounded-xl border border-dashed border-border bg-card px-5 py-8 text-center cursor-pointer hover:border-foreground/20">
              <input
                onChange={(e) => setUploadFile(e.target.files?.[0] ?? null)}
                type="file"
                accept=".zip,application/zip"
                className="hidden"
              />
              <div className="space-y-3">
                <div className="text-sm font-semibold text-foreground">
                  {uploadFile ? uploadFile.name : t("agents.skills.chooseZip")}
                </div>
                <p className="text-xs text-muted-foreground leading-relaxed">
                  {t("agents.skills.zipRequirement")}
                </p>
                <Button variant="ghost" size="sm" className="pointer-events-none">
                  {t("agents.skills.browseFiles")}
                </Button>
              </div>
            </label>
            <ul className="text-xs text-muted-foreground space-y-1.5 list-disc pl-4">
              <li>
                <span className="font-mono">.zip</span> only
              </li>
              <li>{t("agents.skills.singleFolder")}</li>
              <li>
                <span className="font-mono">SKILL.md</span> required
              </li>
            </ul>
            <Button
              onClick={handleUpload}
              disabled={uploading || !uploadFile}
              loading={uploading}
              className="w-full cursor-pointer"
            >
              {t("agents.skills.uploadSkill")}
            </Button>
          </section>
        </div>
      </DialogPopup>
    </Dialog>
  );
}
