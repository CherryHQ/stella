import { useNavigate, useParams, useRouterState } from "@tanstack/react-router";
import { useI18n } from "@/lib/i18n";
import { useAppShell } from "@/layouts/AppShell";
import { useEffect } from "react";
import { cn } from "@/lib/utils";
import { SoulPanel } from "@/features/sessions/panels/SoulPanel";
import { MemoryPanel } from "@/features/sessions/panels/MemoryPanel";

const tabs = [
  { key: "soul", route: "/agents/$agentId/memories/soul" as const },
  { key: "profile", route: "/agents/$agentId/memories/profile" as const },
] as const;

type TabKey = (typeof tabs)[number]["key"];

export function MemoriesPage() {
  const { agentId } = useParams({ strict: false }) as { agentId: string };
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const navigate = useNavigate();
  const { t } = useI18n();
  const { setHeaderTitle } = useAppShell();

  const activeTab: TabKey = pathname.includes("/memories/profile") ? "profile" : "soul";

  const labelMap: Record<TabKey, string> = {
    soul: t("sessions.soul.title"),
    profile: t("sessions.memory.title"),
  };

  useEffect(() => {
    setHeaderTitle(
      <div className="flex items-center gap-1 min-w-0">
        {tabs.map((tab) => (
          <button
            key={tab.key}
            type="button"
            onClick={() => void navigate({ to: tab.route, params: { agentId } })}
            className={cn(
              "px-3 py-1 text-sm font-medium rounded-lg transition-colors cursor-pointer",
              activeTab === tab.key
                ? "bg-accent text-foreground"
                : "text-muted-foreground hover:text-foreground hover:bg-muted/40",
            )}
          >
            {labelMap[tab.key]}
          </button>
        ))}
      </div>,
    );
    return () => setHeaderTitle(null);
  }, [activeTab, agentId, navigate, setHeaderTitle, labelMap]);

  return activeTab === "soul" ? (
    <SoulPanel key={agentId} agentId={agentId} />
  ) : (
    <MemoryPanel key={agentId} agentId={agentId} />
  );
}
