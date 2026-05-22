import { useRef, useState } from "react";
import { useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Bot, PackagePlus, Search, Upload, User } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api } from "@/lib/api";
import { meQueryOptions } from "@/lib/queries/me";
import type { SkillSearchResult } from "@/lib/types";

export function SkillNewPage() {
  const { agentId } = useParams({ from: "/_app/agents/$agentId/skills/new" });
  const { tab } = useSearch({ from: "/_app/agents/$agentId/skills/new" });
  const activeTab = tab ?? "catalog";
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { data: me } = useQuery(meQueryOptions);
  const [scope, setScope] = useState<"user" | "agent">("user");
  const [searchQuery, setSearchQuery] = useState("");
  const [searchResults, setSearchResults] = useState<SkillSearchResult[]>([]);
  const [searching, setSearching] = useState(false);
  const searchTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const [installing, setInstalling] = useState(false);
  const [installTarget, setInstallTarget] = useState("");
  const [installError, setInstallError] = useState("");
  const [uploadFile, setUploadFile] = useState<File | null>(null);
  const [uploading, setUploading] = useState(false);
  const [uploadError, setUploadError] = useState("");

  const canInstallAgentSkill = me?.is_admin ?? false;

  async function openInstalledSkill(res: { id?: string; name?: string }, installedScope = scope) {
    await queryClient.invalidateQueries({ queryKey: ["agent-skills", agentId] });
    if (res?.id) {
      void navigate({
        to: "/agents/$agentId/skills/$scope/$skillId",
        params: { agentId, scope: installedScope, skillId: res.id },
      });
    } else {
      void navigate({ to: "/agents/$agentId/skills", params: { agentId } });
    }
  }

  async function searchSkills(q: string) {
    if (!q.trim()) {
      setSearchResults([]);
      return;
    }
    setSearching(true);
    try {
      const results = await api<SkillSearchResult[]>(
        "GET",
        `/api/skills/search?q=${encodeURIComponent(q)}&limit=20`,
      );
      setSearchResults(results ?? []);
      setInstallError("");
    } catch (e) {
      setInstallError((e as Error).message);
      setSearchResults([]);
    } finally {
      setSearching(false);
    }
  }

  async function installSkill(source: string) {
    const trimmedSource = source.trim();
    if (!trimmedSource) return;
    const installScope = scope;
    setInstalling(true);
    setInstallTarget(trimmedSource);
    setInstallError("");
    try {
      const res = await api<{ id?: string; name?: string }>(
        "POST",
        `/api/agents/${encodeURIComponent(agentId)}/skills/${installScope}/install`,
        { source: trimmedSource },
      );
      await openInstalledSkill(res, installScope);
    } catch (e) {
      setInstallError((e as Error).message);
    } finally {
      setInstalling(false);
      setInstallTarget("");
    }
  }

  async function uploadSkill() {
    if (!uploadFile) return;
    const uploadScope = scope;
    setUploading(true);
    setUploadError("");
    try {
      const form = new FormData();
      form.append("file", uploadFile);
      const res = await api<{ id?: string; name?: string }>(
        "POST",
        `/api/agents/${encodeURIComponent(agentId)}/skills/${uploadScope}/upload`,
        form,
      );
      await openInstalledSkill(res, uploadScope);
    } catch (e) {
      setUploadError((e as Error).message);
    } finally {
      setUploading(false);
    }
  }

  return (
    <div className="flex min-h-0 flex-1 overflow-y-auto bg-muted/20">
      <main className="mx-auto w-full max-w-5xl px-8 py-7">
        <div className="mb-6 flex items-start justify-between gap-6">
          <div className="flex items-start gap-3">
            <div className="mt-0.5 flex size-10 shrink-0 items-center justify-center rounded-lg border border-border bg-background text-primary shadow-xs/5">
              <PackagePlus className="size-5" />
            </div>
            <div className="space-y-1">
              <h2 className="text-lg font-semibold">Install a skill</h2>
              <p className="max-w-[48ch] text-sm leading-5 text-muted-foreground">
                Search the catalog or import a zip bundle into Anna.
              </p>
            </div>
          </div>

          <div className="w-[22rem] shrink-0 rounded-lg border border-border bg-background p-1 shadow-xs/5">
            <div className="grid grid-cols-2 gap-1">
              <Button
                onClick={() => setScope("user")}
                variant={scope === "user" ? "default" : "ghost"}
                size="sm"
                className="justify-start"
              >
                <User className="size-4" />
                My profile
              </Button>
              <Button
                onClick={() => setScope("agent")}
                variant={scope === "agent" ? "default" : "ghost"}
                size="sm"
                className="justify-start"
                disabled={!canInstallAgentSkill}
              >
                <Bot className="size-4" />
                This agent
              </Button>
            </div>
            {!canInstallAgentSkill && (
              <p className="px-2 pt-2 pb-1 text-xs text-muted-foreground">
                Agent installs are available to admins.
              </p>
            )}
          </div>
        </div>

        <div className="rounded-lg border border-border bg-background shadow-xs/5">
          <Tabs
            value={activeTab}
            onValueChange={(nextTab) => {
              void navigate({
                to: "/agents/$agentId/skills/new",
                params: { agentId },
                search: { tab: nextTab === "upload" ? "upload" : "catalog" },
              });
            }}
            className="gap-0"
          >
            <div className="border-b border-border px-5 py-3">
              <TabsList className="grid w-[24rem] grid-cols-2">
                <TabsTrigger value="catalog">
                  <Search className="size-4" />
                  Catalog
                </TabsTrigger>
                <TabsTrigger value="upload">
                  <Upload className="size-4" />
                  Upload zip
                </TabsTrigger>
              </TabsList>
            </div>

            <div className="p-5">
              <TabsContent value="catalog" className="space-y-4">
                <div className="flex items-end justify-between gap-4">
                  <div className="space-y-1">
                    <h3 className="text-base font-semibold">Catalog</h3>
                    <p className="text-sm text-muted-foreground">
                      Search public skills and install one into the selected target.
                    </p>
                  </div>
                </div>
                <Input
                  nativeInput
                  value={searchQuery}
                  onChange={(e) => {
                    const q = (e.target as HTMLInputElement).value;
                    setSearchQuery(q);
                    if (searchTimerRef.current) clearTimeout(searchTimerRef.current);
                    searchTimerRef.current = setTimeout(() => void searchSkills(q), 300);
                  }}
                  type="search"
                  placeholder="Search skills..."
                  size="lg"
                />
                {searching && (
                  <div className="flex justify-center py-16">
                    <Spinner className="size-5" />
                  </div>
                )}
                {!searching && searchResults.length > 0 && (
                  <div className="divide-y divide-border rounded-lg border border-border">
                    {searchResults.map((s) => {
                      const source = `${s.source}@${s.skillId}`;
                      return (
                        <div key={s.id} className="p-4">
                          <div className="flex items-start justify-between gap-5">
                            <div className="min-w-0">
                              <div className="flex items-center gap-2">
                                <p className="truncate font-mono text-sm font-medium">
                                  {s.name || s.skillId}
                                </p>
                                <span className="shrink-0 text-xs text-muted-foreground">
                                  {s.installs} installs
                                </span>
                              </div>
                              <p className="mt-1 truncate font-mono text-xs text-muted-foreground">
                                {source}
                              </p>
                            </div>
                            <Button
                              onClick={() => void installSkill(source)}
                              disabled={installing}
                              loading={installing && installTarget === source}
                              size="sm"
                            >
                              Install
                            </Button>
                          </div>
                          {s.description && (
                            <p className="mt-3 max-w-3xl text-sm leading-5 text-muted-foreground">
                              {s.description}
                            </p>
                          )}
                        </div>
                      );
                    })}
                  </div>
                )}
                {!searching && searchResults.length === 0 && searchQuery && (
                  <div className="rounded-lg border border-dashed border-border px-5 py-14 text-center text-sm text-muted-foreground">
                    No skills found for that search.
                  </div>
                )}
                {!searching && !searchQuery && (
                  <div className="rounded-lg border border-dashed border-border px-5 py-16 text-center">
                    <Search className="mx-auto mb-3 size-5 text-muted-foreground" />
                    <p className="text-sm font-medium">Search the catalog</p>
                    <p className="mt-1 text-sm text-muted-foreground">
                      Results appear here as you type.
                    </p>
                  </div>
                )}
                {installError && <p className="text-sm text-destructive">{installError}</p>}
              </TabsContent>

              <TabsContent value="upload" className="space-y-4">
                <div className="space-y-1">
                  <h3 className="text-base font-semibold">Upload zip</h3>
                  <p className="text-sm text-muted-foreground">Import a skill bundle from disk.</p>
                </div>

                <label className="block cursor-pointer rounded-lg border-2 border-dashed border-border px-5 py-14 text-center transition-colors hover:border-primary/70">
                  <input
                    onChange={(e) => {
                      setUploadFile(e.target.files?.[0] ?? null);
                      setUploadError("");
                    }}
                    type="file"
                    accept=".zip,application/zip"
                    className="hidden"
                  />
                  <Upload className="mx-auto mb-3 size-7 text-muted-foreground" />
                  <div className="text-sm font-medium">
                    {uploadFile ? uploadFile.name : "Choose a .zip file"}
                  </div>
                  <p className="mt-1 text-xs text-muted-foreground">
                    Exactly one skill folder with <span className="font-mono">SKILL.md</span>.
                  </p>
                  <Button variant="outline" size="sm" className="mt-4">
                    Browse files
                  </Button>
                </label>

                <div className="grid grid-cols-3 gap-2 text-center text-xs text-muted-foreground">
                  <div className="rounded-lg border border-border px-2 py-2 font-mono">.zip</div>
                  <div className="rounded-lg border border-border px-2 py-2">One folder</div>
                  <div className="rounded-lg border border-border px-2 py-2 font-mono">
                    SKILL.md
                  </div>
                </div>

                {uploadError && <p className="text-sm text-destructive">{uploadError}</p>}

                <Button
                  onClick={() => void uploadSkill()}
                  disabled={uploading || !uploadFile}
                  loading={uploading}
                  className="w-full"
                >
                  Upload skill
                </Button>
              </TabsContent>
            </div>
          </Tabs>
        </div>
      </main>
    </div>
  );
}
