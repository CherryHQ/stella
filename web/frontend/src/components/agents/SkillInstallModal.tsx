import { useRef, useState } from "react";
import { api } from "@/lib/api";
import type { SkillSearchResult } from "@/lib/types";
import type { AgentsPageState } from "./AgentsPage";

interface Props {
  state: AgentsPageState;
  onClose: () => void;
  onSetScope: (scope: "user" | "agent") => void;
  onInstall: (source: string, scope: "user" | "agent") => Promise<void>;
  onUpload: (file: File, scope: "user" | "agent") => Promise<void>;
  showToast: (msg: string, type?: "success" | "error") => void;
}

export function SkillInstallModal({ state, onClose, onSetScope, onInstall, onUpload, showToast }: Props) {
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
    if (!q.trim()) { setSearchResults([]); return; }
    setSearching(true);
    try {
      const results = await api<SkillSearchResult[]>("GET", `/api/skills/search?q=${encodeURIComponent(q)}&limit=20`);
      setSearchResults(results ?? []);
    } catch (e) {
      showToast((e as Error).message, "error");
      setSearchResults([]);
    } finally {
      setSearching(false); }
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
    <div className="modal modal-open">
      <div className="modal-box w-11/12 max-w-5xl p-0 overflow-hidden">
        <div className="border-b border-base-300 px-6 py-5 flex items-start justify-between gap-4">
          <div className="space-y-1">
            <h3 className="font-bold text-xl">Add a skill</h3>
            <p className="text-sm text-base-content/60">Install from the catalog or upload your own skill bundle.</p>
          </div>
          <button onClick={onClose} className="btn btn-ghost btn-sm btn-circle shrink-0">✕</button>
        </div>
        <div className="px-6 py-4 border-b border-base-300 space-y-2 bg-base-200/30">
          <label className="text-xs font-medium text-secondary block uppercase tracking-wide">Install target</label>
          <div className="flex items-center gap-2 flex-wrap">
            {canInstallAgentSkills && (
              <button
                onClick={() => onSetScope("agent")}
                type="button"
                className={`btn btn-sm ${skillInstallScope === "agent" ? "btn-primary" : "btn-ghost"}`}
              >
                Only this agent
              </button>
            )}
            <button
              onClick={() => onSetScope("user")}
              type="button"
              className={`btn btn-sm ${skillInstallScope === "user" ? "btn-primary" : "btn-ghost"}`}
            >
              My profile
            </button>
          </div>
          {!canInstallAgentSkills && (
            <p className="text-xs text-base-content/50">
              Agent-only install is available after the agent is saved, and only for admins.
            </p>
          )}
        </div>
        <div className="grid grid-cols-1 xl:grid-cols-[minmax(0,1.3fr)_minmax(320px,0.9fr)] gap-0">
          <section className="p-6 min-w-0 xl:border-r border-base-300 space-y-4">
            <div className="space-y-1">
              <h4 className="font-medium text-base">Browse catalog</h4>
              <p className="text-sm text-base-content/60">Search public skills and install in one click.</p>
            </div>
            <input
              value={searchQuery}
              onChange={(e) => {
                const q = e.target.value;
                setSearchQuery(q);
                if (searchTimerRef.current) clearTimeout(searchTimerRef.current);
                searchTimerRef.current = setTimeout(() => doSearch(q), 300);
              }}
              type="text"
              placeholder="Search skills..."
              className="input input-bordered w-full"
              autoFocus
            />
            {searching && (
              <div className="py-10 text-center">
                <span className="loading loading-spinner loading-md"></span>
              </div>
            )}
            {!searching && searchResults.length > 0 && (
              <div className="space-y-3 max-h-[28rem] overflow-y-auto pr-1">
                {searchResults.map((s) => (
                  <div key={s.id} className="rounded-box border border-base-300 bg-base-100 px-4 py-4 space-y-3">
                    <div className="flex items-start justify-between gap-3">
                      <div className="min-w-0">
                        <div className="flex items-center gap-2 flex-wrap">
                          <p className="font-mono text-sm font-medium truncate">{s.name || s.skillId}</p>
                          <span className="badge badge-ghost badge-xs">{s.installs} installs</span>
                        </div>
                        <p className="text-xs text-base-content/50 font-mono truncate mt-1">
                          {s.source}@{s.skillId}
                        </p>
                      </div>
                      <button
                        onClick={() => handleInstall(`${s.source}@${s.skillId}`)}
                        disabled={installing}
                        type="button"
                        className="btn btn-primary btn-sm shrink-0"
                      >
                        {installing && installSource === `${s.source}@${s.skillId}` && (
                          <span className="loading loading-spinner loading-xs"></span>
                        )}
                        Install
                      </button>
                    </div>
                    {s.description && <p className="text-sm text-base-content/70">{s.description}</p>}
                  </div>
                ))}
              </div>
            )}
            {!searching && searchResults.length === 0 && searchQuery && (
              <div className="rounded-box border border-dashed border-base-300 p-8 text-center text-sm text-base-content/60">
                No skills found for that search.
              </div>
            )}
            {!searching && !searchQuery && (
              <div className="rounded-box border border-dashed border-base-300 p-8 text-center text-sm text-base-content/60">
                Start typing to search the catalog.
              </div>
            )}
            <div className="space-y-1">
              <h4 className="font-medium text-sm">Or install from source</h4>
              <div className="flex gap-2">
                <input
                  value={installSource}
                  onChange={(e) => setInstallSource(e.target.value)}
                  type="text"
                  placeholder="source@skill-id"
                  className="input input-bordered input-sm flex-1 font-mono"
                />
                <button
                  onClick={() => handleInstall(installSource)}
                  disabled={installing || !installSource}
                  type="button"
                  className="btn btn-primary btn-sm"
                >
                  {installing && <span className="loading loading-spinner loading-xs"></span>}
                  Install
                </button>
              </div>
            </div>
          </section>
          <section className="p-6 space-y-4 bg-base-200/20">
            <div className="space-y-1">
              <h4 className="font-medium text-base">Upload zip</h4>
              <p className="text-sm text-base-content/60">Import a skill you already have on disk.</p>
            </div>
            <label className="block rounded-box border-2 border-dashed border-base-300 bg-base-100 px-5 py-8 text-center cursor-pointer hover:border-primary transition-colors">
              <input
                onChange={(e) => setUploadFile(e.target.files?.[0] ?? null)}
                type="file"
                accept=".zip,application/zip"
                className="hidden"
              />
              <div className="space-y-2">
                <div className="text-sm font-medium">{uploadFile ? uploadFile.name : "Choose a .zip file"}</div>
                <p className="text-xs text-base-content/60">
                  Must contain exactly one skill folder with <span className="font-mono">SKILL.md</span>.
                </p>
                <span className="btn btn-ghost btn-sm">Browse files</span>
              </div>
            </label>
            <ul className="text-xs text-base-content/60 space-y-1 list-disc pl-4">
              <li><span className="font-mono">.zip</span> only</li>
              <li>Single skill folder</li>
              <li><span className="font-mono">SKILL.md</span> required</li>
            </ul>
            <button
              onClick={handleUpload}
              disabled={uploading || !uploadFile}
              type="button"
              className="btn btn-primary w-full"
            >
              {uploading && <span className="loading loading-spinner loading-xs"></span>}
              Upload skill
            </button>
          </section>
        </div>
      </div>
      <div className="modal-backdrop" onClick={onClose}></div>
    </div>
  );
}
