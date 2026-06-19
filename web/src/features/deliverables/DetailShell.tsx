import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ArrowLeft } from "lucide-react";
import { agentsQueryOptions } from "@/lib/queries/agents";
import { getAgentColor } from "@/lib/agent-colors";
import { useI18n } from "@/lib/i18n";
import { avatarInitials } from "@/features/deliverables/lib";

interface DetailShellProps {
  agentId: string;
  kindLabel: string;
  title: React.ReactNode;
  pill?: React.ReactNode;
  actions?: React.ReactNode;
  children: React.ReactNode;
}

/** Full-width detail page chrome: back link, kind crumb, title row, actions. */
export function DetailShell({
  agentId,
  kindLabel,
  title,
  pill,
  actions,
  children,
}: DetailShellProps) {
  const { t } = useI18n();
  return (
    <div className="h-full min-h-0 overflow-y-auto bg-background">
      <div className="mx-auto max-w-[800px] px-6 py-7 pb-20 sm:px-8">
        <Link
          to="/agents/$agentId/deliverables"
          params={{ agentId }}
          className="inline-flex items-center gap-1.5 text-[12.5px] font-medium text-muted-foreground hover:text-foreground"
        >
          <ArrowLeft className="size-3.5" />
          {t("hub.title")}
        </Link>
        <div className="mt-4 font-mono text-xs font-medium text-muted-foreground">{kindLabel}</div>
        <div className="mt-1.5 flex flex-wrap items-start justify-between gap-x-4 gap-y-3">
          <h2 className="flex min-w-0 flex-wrap items-center gap-2.5 text-[22px] font-semibold tracking-tight leading-snug">
            {title}
            {pill}
          </h2>
          {actions && <div className="flex shrink-0 flex-wrap gap-2 pt-1">{actions}</div>}
        </div>
        {children}
      </div>
    </div>
  );
}

export function DetailSection({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="mt-7">
      <h3 className="mb-2.5 font-mono text-xs font-semibold uppercase tracking-[0.08em] text-muted-foreground">
        {title}
      </h3>
      {children}
    </div>
  );
}

export function MetaSep() {
  return <span className="text-border">·</span>;
}

/** Small avatar + display name for an agent — never the raw UUID. */
export function AgentChip({ agentId }: { agentId: string }) {
  const { data: agents = [] } = useQuery(agentsQueryOptions);
  const agent = agents.find((a) => a.id === agentId);
  const name = agent?.name || agentId;
  return (
    <span className="inline-flex items-center gap-1.5">
      <span
        className="grid size-4 place-items-center rounded-full text-xs font-semibold text-background"
        style={{ backgroundColor: getAgentColor(agentId) }}
      >
        {avatarInitials(name)}
      </span>
      <span className="font-medium text-foreground">{name}</span>
    </span>
  );
}
