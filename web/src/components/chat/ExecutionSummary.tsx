import type { SessionExecutionSummary } from "@/lib/api-client/types.gen";
import { Button } from "@/components/ui/button";
import { Collapsible, CollapsiblePanel, CollapsibleTrigger } from "@/components/ui/collapsible";
import { useI18n } from "@/lib/i18n";

export function ExecutionSummaryPanel({ summary }: { summary: SessionExecutionSummary }) {
  const { t } = useI18n();
  const unknown = t("chat.executionUnknown");
  const states = {
    unknown,
    ready: t("chat.executionReady"),
    unavailable: t("chat.executionUnavailable"),
    not_required: t("chat.executionNotRequired"),
    selected: t("chat.executionSelected"),
    masked: t("chat.executionMasked"),
    overridden: t("chat.executionOverridden"),
  };

  return (
    <Collapsible className="w-full max-w-[85%] break-words">
      <CollapsibleTrigger render={<Button variant="ghost" size="xs" />}>
        {t("chat.executionSummary")} · {summary.plugins.length} {t("chat.executionPlugins")} ·{" "}
        {summary.skills?.length ?? 0} {t("chat.executionSkills")}
      </CollapsibleTrigger>
      <CollapsiblePanel className="pt-2">
        <div className="space-y-3">
          {summary.plugins.length > 0 && (
            <section className="space-y-2">
              <div className="text-foreground">{t("chat.executionPlugins")}</div>
              {summary.plugins.map((plugin) => (
                <div key={plugin.plugin_id} className="space-y-1 border-l border-border/40 pl-2">
                  <div className="text-foreground">{plugin.plugin_id}</div>
                  <div className="flex flex-wrap gap-x-3 gap-y-1">
                    <span>
                      {t("chat.executionReadiness")}: {states[plugin.readiness] ?? unknown}
                    </span>
                    <span>
                      {t("chat.executionAuthorization")}: {states[plugin.authorization] ?? unknown}
                    </span>
                    <span>
                      {t("chat.executionSource")}: {plugin.source ?? unknown}
                    </span>
                  </div>
                  <div>
                    {t("chat.executionPackage")}: {plugin.package_version ?? unknown} ·{" "}
                    {plugin.package_digest ?? unknown}
                  </div>
                  <div>
                    {t("chat.executionConfig")}: {plugin.config_id ?? unknown} ·{" "}
                    {plugin.config_scope ?? unknown} · {plugin.config_revision ?? unknown}
                  </div>
                  {plugin.binaries?.map((binary) => (
                    <div key={`${plugin.plugin_id}:${binary.name}`} className="space-y-0.5 pl-2">
                      <div className="text-foreground">{binary.name}</div>
                      <div className="flex flex-wrap gap-x-3 gap-y-1">
                        <span>
                          {t("chat.executionRequested")}: {binary.requested_version ?? unknown}
                        </span>
                        <span>
                          {t("chat.executionResolved")}: {binary.resolved_version ?? unknown}
                        </span>
                        <span>
                          {t("chat.executionBackend")}: {binary.backend ?? unknown}
                        </span>
                        <span>
                          {t("chat.executionSelection")}: {binary.selection_identity ?? unknown}
                        </span>
                      </div>
                    </div>
                  ))}
                  {plugin.failures?.map((failure, index) => (
                    <div
                      key={`${plugin.plugin_id}:failure:${index}`}
                      className="text-destructive-foreground"
                    >
                      {t("chat.executionFailure")}: {failure}
                    </div>
                  ))}
                </div>
              ))}
            </section>
          )}
          {summary.skills && summary.skills.length > 0 && (
            <section className="space-y-2">
              <div className="text-foreground">{t("chat.executionSkills")}</div>
              {summary.skills.map((skill, index) => (
                <div
                  key={`${skill.plugin_id ?? "skill"}:${skill.name}:${index}`}
                  className="flex flex-wrap gap-x-3 gap-y-1 pl-2"
                >
                  <span className="text-foreground">{skill.name}</span>
                  <span>
                    {t("chat.executionState")}: {states[skill.state ?? "unknown"] ?? unknown}
                  </span>
                  <span>
                    {t("chat.executionSource")}: {skill.source ?? unknown}
                  </span>
                  <span>
                    {t("chat.executionVersion")}: {skill.version ?? unknown}
                  </span>
                  <span>
                    {t("chat.executionScope")}: {skill.scope ?? unknown}
                  </span>
                  <span>
                    {t("chat.executionDigest")}: {skill.digest ?? unknown}
                  </span>
                </div>
              ))}
            </section>
          )}
        </div>
      </CollapsiblePanel>
    </Collapsible>
  );
}
