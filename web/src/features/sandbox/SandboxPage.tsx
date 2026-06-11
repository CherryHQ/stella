import { useMemo } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { togglePlugin as togglePluginRequest } from "@/lib/api-client/sdk.gen";
import type { Plugin } from "@/lib/types";
import { pluginLabel, pluginDescription, sandboxMeta } from "@/features/plugins/pluginUtils";
import { pluginsQueryOptions } from "@/lib/queries/plugins";
import { Badge } from "@/components/ui/badge";
import { Switch } from "@/components/ui/switch";
import { useI18n } from "@/lib/i18n";
import { useToast, ToastContainer } from "@/hooks/use-toast";

const validSandboxBackends = new Set(["sandbox/docker", "sandbox/local", "sandbox/none"]);

export function SandboxPage() {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { data: allPlugins = [] } = useQuery(pluginsQueryOptions);
  const { toasts, showToast } = useToast(4000);

  const sandboxPlugins = useMemo(
    () =>
      allPlugins
        .filter((p) => p.kind === "sandbox" && validSandboxBackends.has(p.id))
        .map((p) => ({ ...p, capabilities: Array.isArray(p.capabilities) ? p.capabilities : [] })),
    [allPlugins],
  );

  const envLocked = sandboxPlugins.some((p) => p.env_locked);

  async function toggleSandbox(plugin: Plugin, enabled: boolean) {
    try {
      if (enabled) {
        for (const other of sandboxPlugins.filter((p) => p.id !== plugin.id && p.enabled)) {
          await togglePluginRequest({
            path: { kind: other.kind, name: other.name },
            body: { enabled: false },
            throwOnError: true,
          });
        }
      }
      const { data } = await togglePluginRequest({
        path: { kind: plugin.kind, name: plugin.name },
        body: { enabled },
        throwOnError: true,
      });
      const updated = data as Plugin;
      const updatedEnabled = !!updated.enabled;
      showToast(
        enabled
          ? plugin.id + " set as active sandbox"
          : updatedEnabled
            ? "At least one sandbox must stay active"
            : plugin.id + " disabled",
      );
      void queryClient.invalidateQueries({ queryKey: ["plugins"] });
    } catch (e) {
      showToast((e as Error).message, "error");
      void queryClient.invalidateQueries({ queryKey: ["plugins"] });
    }
  }

  return (
    <>
      <div className="h-full overflow-y-auto">
        <div className="mx-auto max-w-2xl p-6 space-y-6">
          <div className="space-y-1">
            <h2 className="text-lg font-semibold tracking-tight text-foreground">
              {t("settings.nav.sandbox")}
            </h2>
            <p className="text-xs text-muted-foreground">
              Choose how agent code execution is isolated.
            </p>
            {envLocked && (
              <p className="text-xs text-warning-foreground">
                Locked by STELLA_SANDBOX_BACKEND environment variable.
              </p>
            )}
          </div>

          <div className="space-y-3">
            {sandboxPlugins.map((p) => {
              const meta = sandboxMeta(p.id);
              const active = !!p.enabled;
              return (
                <div
                  key={p.id}
                  className={`rounded-xl border p-5 space-y-3 transition-colors duration-120 ${
                    active ? "border-primary/40 bg-primary/4" : "border-border bg-card"
                  }`}
                >
                  <div className="flex items-start justify-between gap-3">
                    <div className="space-y-1">
                      <div className="flex items-center gap-2">
                        <span className="text-sm font-semibold text-foreground">
                          {pluginLabel(p)}
                        </span>
                        {active && (
                          <Badge variant="success" size="sm">
                            active
                          </Badge>
                        )}
                        {meta.recommended && (
                          <Badge variant="default" size="sm">
                            recommended
                          </Badge>
                        )}
                        {meta.isDefault && (
                          <Badge variant="secondary" size="sm">
                            default
                          </Badge>
                        )}
                      </div>
                      {pluginDescription(p) && (
                        <p className="text-xs text-muted-foreground">{pluginDescription(p)}</p>
                      )}
                    </div>
                    <Switch
                      checked={active}
                      disabled={envLocked}
                      onCheckedChange={(checked) => void toggleSandbox(p, checked)}
                    />
                  </div>

                  {meta.features.length > 0 && (
                    <ul className="text-xs text-muted-foreground space-y-1">
                      {meta.features.map((f) => (
                        <li key={f} className="flex items-start gap-2">
                          <span className="text-success-foreground shrink-0 font-semibold mt-px">
                            ✓
                          </span>
                          <span>{f}</span>
                        </li>
                      ))}
                    </ul>
                  )}

                  {meta.limitations.length > 0 && (
                    <ul className="text-xs text-muted-foreground space-y-1">
                      {meta.limitations.map((l) => (
                        <li key={l} className="flex items-start gap-2">
                          <span className="text-warning-foreground shrink-0 font-semibold mt-px">
                            ⚠
                          </span>
                          <span>{l}</span>
                        </li>
                      ))}
                    </ul>
                  )}
                </div>
              );
            })}
          </div>
        </div>
      </div>
      <ToastContainer messages={toasts} />
    </>
  );
}
