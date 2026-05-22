import { useNavigate, useParams } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { agentSkillsOptions } from "@/lib/queries/agents";
import { cn } from "@/lib/utils";
import { useI18n } from "@/lib/i18n";
import { Button } from "@/components/ui/button";

export function SkillsListPage() {
  const { agentId } = useParams({ from: "/_app/agents/$agentId/skills/" });
  const navigate = useNavigate();
  const { t } = useI18n();
  const { data: skills = [], isLoading } = useQuery(agentSkillsOptions(agentId));

  const systemSkills = skills.filter((s) => s.scope === "system");
  const agentSkills = skills.filter((s) => s.scope === "agent");
  const userSkills = skills.filter((s) => s.scope === "user");

  return (
    <div className="flex-1 min-w-0 flex flex-col overflow-hidden">
      <div className="flex-shrink-0 h-12 px-5 border-b border-border/60 bg-background flex items-center gap-3">
        <h2 className="text-[15px] font-medium tracking-tight">{t("sessions.sidebar.skills")}</h2>
        <div className="ml-auto">
          <Button
            size="sm"
            onClick={() =>
              void navigate({ to: "/agents/$agentId/skills/new", params: { agentId } })
            }
            className="rounded-xl text-xs gap-1.5"
          >
            <svg
              className="w-3 h-3"
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              strokeWidth="2.5"
            >
              <path d="M12 5v14M5 12h14" />
            </svg>
            New Skill
          </Button>
        </div>
      </div>

      {isLoading ? (
        <div className="flex-1 flex items-center justify-center">
          <div className="w-4 h-4 border-2 border-muted-foreground/30 border-t-muted-foreground rounded-full animate-spin" />
        </div>
      ) : skills.length === 0 ? (
        <div className="flex-1 flex items-center justify-center">
          <p className="text-sm text-muted-foreground/50 font-mono">No skills yet</p>
        </div>
      ) : (
        <div className="flex-1 overflow-y-auto p-4">
          <div className="max-w-3xl space-y-6">
            {userSkills.length > 0 && (
              <SkillGroup
                label="User"
                skills={userSkills}
                agentId={agentId}
                onSelect={(id) =>
                  void navigate({
                    to: "/agents/$agentId/skills/$scope/$skillId",
                    params: { agentId, scope: "user", skillId: id },
                  })
                }
              />
            )}
            {agentSkills.length > 0 && (
              <SkillGroup
                label="Agent"
                skills={agentSkills}
                agentId={agentId}
                onSelect={(id) =>
                  void navigate({
                    to: "/agents/$agentId/skills/$scope/$skillId",
                    params: { agentId, scope: "agent", skillId: id },
                  })
                }
              />
            )}
            {systemSkills.length > 0 && (
              <SkillGroup
                label="System"
                skills={systemSkills}
                agentId={agentId}
                onSelect={(id) =>
                  void navigate({
                    to: "/agents/$agentId/skills/$scope/$skillId",
                    params: { agentId, scope: "system", skillId: id },
                  })
                }
              />
            )}
          </div>
        </div>
      )}
    </div>
  );
}

function SkillGroup({
  label,
  skills,
  onSelect,
}: {
  label: string;
  skills: { id: string; name: string; description: string; scope: string; status?: string }[];
  agentId: string;
  onSelect: (id: string) => void;
}) {
  return (
    <div>
      <p className="text-[10px] font-mono font-medium uppercase tracking-wider text-muted-foreground/50 mb-2">
        {label}
      </p>
      <div className="grid grid-cols-1 lg:grid-cols-2 gap-3">
        {skills.map((s) => (
          <div
            key={s.id}
            onClick={() => onSelect(s.id)}
            className={cn(
              "rounded-xl border border-border/60 bg-card p-4 cursor-pointer hover:shadow-sm transition-all duration-150",
            )}
          >
            <div className="flex items-center gap-2 min-w-0">
              <p className="text-[13px] font-medium truncate">{s.name}</p>
              {s.status && s.status !== "active" && (
                <span className="shrink-0 rounded-full bg-muted px-1.5 py-0.5 text-[9px] font-medium uppercase tracking-wide text-muted-foreground">
                  {s.status}
                </span>
              )}
            </div>
            {s.description && (
              <p className="text-[11px] text-muted-foreground/60 mt-1 line-clamp-2">
                {s.description}
              </p>
            )}
          </div>
        ))}
      </div>
    </div>
  );
}
