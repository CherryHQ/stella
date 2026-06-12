import { useMemo, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Check, Download, Info, Search } from "lucide-react";
import { useToast, ToastContainer } from "@/hooks/use-toast";
import { installAgentSkill } from "@/lib/api-client/sdk.gen";
import { useI18n } from "@/lib/i18n";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  MOCK_DISCOVER_SKILLS,
  type DiscoverSkill,
} from "@/features/sessions/skills/skill-mock-discover";

function formatInstalls(n: number): string {
  return n >= 1000 ? `${(n / 1000).toFixed(1).replace(/\.0$/, "")}k` : String(n);
}

export function SkillsDiscover({
  agentId,
  installedNames,
}: {
  agentId: string;
  installedNames: Set<string>;
}) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { toasts, showToast } = useToast();
  const [query, setQuery] = useState("");
  const [installingSlug, setInstallingSlug] = useState<string | null>(null);

  const rows = useMemo(() => {
    const q = query.trim().toLowerCase();
    return MOCK_DISCOVER_SKILLS.filter(
      (s) => !q || s.name.toLowerCase().includes(q) || s.summary.toLowerCase().includes(q),
    );
  }, [query]);

  async function install(skill: DiscoverSkill) {
    setInstallingSlug(skill.slug);
    try {
      await installAgentSkill({
        path: { id: agentId },
        body: { source: `clawhub:${skill.slug}`, scope: "user" },
        throwOnError: true,
      });
      showToast(t("sessions.discover.installSuccess"), "success");
      void queryClient.invalidateQueries({ queryKey: ["agent-skills", agentId] });
    } catch (error) {
      showToast(error instanceof Error ? error.message : t("common.error"), "error");
    } finally {
      setInstallingSlug(null);
    }
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-2 text-xs text-muted-foreground">
        <Info size={16} />
        <span>{t("sessions.discover.previewNote")}</span>
      </div>
      <div className="relative max-w-sm">
        <Search
          className="absolute left-3 top-1/2 -translate-y-1/2 text-muted-foreground"
          size={16}
        />
        <Input
          nativeInput
          type="search"
          value={query}
          onChange={(e) => setQuery((e.target as HTMLInputElement).value)}
          placeholder={t("sessions.discover.searchPlaceholder")}
          className="pl-9"
        />
      </div>
      <div className="space-y-1">
        {rows.map((skill) => {
          const installed = installedNames.has(skill.name);
          return (
            <div
              key={skill.slug}
              className="w-full rounded-lg px-3 py-2.5 hover:bg-muted md:flex md:min-h-13 md:items-center md:gap-3 md:py-0"
            >
              <div className="flex items-center gap-2 md:min-w-0 md:flex-1 md:gap-3">
                <span className="shrink-0 font-mono text-sm">{skill.name}</span>
                <Badge variant="outline" size="sm">
                  v{skill.version}
                </Badge>
                <p className="hidden min-w-0 flex-1 truncate text-xs text-muted-foreground md:block">
                  {skill.summary}
                </p>
              </div>
              <p className="mt-0.5 truncate text-xs text-muted-foreground md:hidden">
                {skill.summary}
              </p>
              <div className="mt-1.5 flex shrink-0 items-center gap-3 md:mt-0">
                <span className="inline-flex items-center gap-1 text-xs text-muted-foreground">
                  <Download className="size-3.5" />
                  {t("sessions.discover.installs", { n: formatInstalls(skill.installs) })}
                </span>
                {installed ? (
                  <Badge variant="success" size="sm">
                    <Check size={16} />
                    {t("sessions.discover.installed")}
                  </Badge>
                ) : (
                  <Button
                    size="sm"
                    variant="outline"
                    loading={installingSlug === skill.slug}
                    disabled={installingSlug !== null}
                    onClick={() => void install(skill)}
                  >
                    {t("common.install")}
                  </Button>
                )}
              </div>
            </div>
          );
        })}
        {rows.length === 0 && (
          <div className="p-8 text-center text-sm text-muted-foreground">
            {t("sessions.discover.noResults")}
          </div>
        )}
      </div>
      <ToastContainer messages={toasts} />
    </div>
  );
}
