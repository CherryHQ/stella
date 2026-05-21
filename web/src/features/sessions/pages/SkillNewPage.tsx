import { useState } from "react";
import { useNavigate, useParams } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { SkillPanel } from "@/features/sessions/panels/SkillPanel";
import { Button } from "@/components/ui/button";
import { api } from "@/lib/api";
import { meQueryOptions } from "@/lib/queries/me";

export function SkillNewPage() {
  const { agentId } = useParams({ from: "/_app/agents/$agentId/skills/new" });
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { data: me } = useQuery(meQueryOptions);
  const [scope, setScope] = useState<"user" | "agent">("user");
  const [uploadFile, setUploadFile] = useState<File | null>(null);
  const [uploading, setUploading] = useState(false);
  const [uploadError, setUploadError] = useState("");

  const canUploadAgentSkill = me?.is_admin ?? false;

  async function uploadSkill() {
    if (!uploadFile) return;
    setUploading(true);
    setUploadError("");
    try {
      const form = new FormData();
      form.append("file", uploadFile);
      const res = await api<{ id?: string; name?: string }>(
        "POST",
        `/api/agents/${encodeURIComponent(agentId)}/skills/${scope}/upload`,
        form,
      );
      await queryClient.invalidateQueries({ queryKey: ["agent-skills", agentId] });
      if (res?.id) {
        void navigate({
          to: "/agents/$agentId/skills/$scope/$skillId",
          params: { agentId, scope, skillId: res.id },
        });
      } else {
        void navigate({ to: "/agents/$agentId/skills", params: { agentId } });
      }
    } catch (e) {
      setUploadError((e as Error).message);
    } finally {
      setUploading(false);
    }
  }

  return (
    <div className="flex flex-1 min-h-0 overflow-hidden">
      <div className="min-w-0 flex-1 overflow-hidden border-r border-border">
        <SkillPanel
          skillId={null}
          agentId={agentId}
          onSaved={() => {
            void queryClient.invalidateQueries({ queryKey: ["agent-skills", agentId] });
            void navigate({ to: "/agents/$agentId/skills", params: { agentId } });
          }}
          onDeleted={() => {
            void queryClient.invalidateQueries({ queryKey: ["agent-skills", agentId] });
            void navigate({ to: "/agents/$agentId/skills", params: { agentId } });
          }}
        />
      </div>
      <aside className="w-[22rem] shrink-0 overflow-y-auto bg-muted/20 p-5">
        <div className="space-y-5 rounded-xl border border-border bg-background p-5">
          <div className="space-y-1">
            <h3 className="text-base font-semibold">Upload skill zip</h3>
            <p className="text-sm text-muted-foreground">
              Import a skill bundle instead of creating one manually.
            </p>
          </div>

          <div className="space-y-2">
            <label className="block text-xs font-medium uppercase tracking-wide text-muted-foreground">
              Upload target
            </label>
            <div className="flex flex-wrap gap-2">
              <Button
                onClick={() => setScope("user")}
                variant={scope === "user" ? "default" : "ghost"}
                size="sm"
              >
                My profile
              </Button>
              {canUploadAgentSkill && (
                <Button
                  onClick={() => setScope("agent")}
                  variant={scope === "agent" ? "default" : "ghost"}
                  size="sm"
                >
                  Only this agent
                </Button>
              )}
            </div>
            {!canUploadAgentSkill && (
              <p className="text-xs text-muted-foreground">
                Agent-only upload is available to admins.
              </p>
            )}
          </div>

          <label className="block cursor-pointer rounded-xl border-2 border-dashed border-border bg-background px-5 py-8 text-center transition-colors hover:border-primary">
            <input
              onChange={(e) => {
                setUploadFile(e.target.files?.[0] ?? null);
                setUploadError("");
              }}
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

          <ul className="list-disc space-y-1 pl-4 text-xs text-muted-foreground">
            <li>
              <span className="font-mono">.zip</span> only
            </li>
            <li>Single skill folder</li>
            <li>
              <span className="font-mono">SKILL.md</span> required
            </li>
          </ul>

          {uploadError && <p className="text-sm text-destructive">{uploadError}</p>}

          <Button
            onClick={() => void uploadSkill()}
            disabled={uploading || !uploadFile}
            loading={uploading}
            className="w-full"
          >
            Upload skill
          </Button>
        </div>
      </aside>
    </div>
  );
}
