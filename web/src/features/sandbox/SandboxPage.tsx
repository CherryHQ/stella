import { useCallback, useEffect, useState } from "react";
import { listPlugins, togglePlugin as togglePluginRequest } from "@/lib/api-client/sdk.gen";
import type { Plugin } from "@/lib/types";
import { pluginDescription, pluginLabel, sandboxMeta } from "@/features/plugins/pluginUtils";
import { Badge } from "@/components/ui/badge";
import { Switch } from "@/components/ui/switch";
import { useToast, ToastContainer } from "@/hooks/use-toast";
import { SettingsPageHeader } from "@/features/settings/SettingsPageHeader";
import { Box } from "lucide-react";

const validSandboxBackends = new Set(["sandbox/docker", "sandbox/local", "sandbox/none"]);

export function SandboxPage() {
  const [plugins, setPlugins] = useState<Plugin[]>([]);
  const { toasts, showToast } = useToast(4000);

  const sandboxPlugins = plugins.filter(
    (p) => p.kind === "sandbox" && validSandboxBackends.has(p.id),
  );

  const loadPlugins = useCallback(async () => {
    try {
      const { data } = await listPlugins({ throwOnError: true });
      setPlugins(
        ((data?.plugins as Plugin[]) ?? []).map((p) => ({
          ...p,
          capabilities: Array.isArray(p.capabilities) ? p.capabilities : [],
        })),
      );
    } catch (e) {
      showToast((e as Error).message, "error");
    }
  }, []);

  useEffect(() => {
    void loadPlugins();
  }, [loadPlugins]);

  function updateEnabled(id: string, enabled: boolean) {
    setPlugins((prev) => prev.map((p) => (p.id === id ? { ...p, enabled } : p)));
  }

  async function toggleSandbox(id: string, enabled: boolean) {
    const previous = new Map(sandboxPlugins.map((p) => [p.id, p.enabled]));
    try {
      if (enabled) {
        for (const other of sandboxPlugins.filter((p) => p.id !== id && p.enabled)) {
          updateEnabled(other.id, false);
          await togglePluginRequest({
            path: { kind: other.kind, name: other.name },
            body: { enabled: false },
            throwOnError: true,
          });
        }
      }
      updateEnabled(id, enabled);
      const plugin = plugins.find((p) => p.id === id);
      if (!plugin) return;
      const { data } = await togglePluginRequest({
        path: { kind: plugin.kind, name: plugin.name },
        body: { enabled },
        throwOnError: true,
      });
      const updated = data as Plugin;
      const updatedEnabled = !!updated.enabled;
      updateEnabled(updated.id || id, updatedEnabled);
      showToast(
        enabled
          ? id + " set as active sandbox"
          : updatedEnabled
            ? "At least one sandbox must stay active"
            : id + " disabled",
      );
      void loadPlugins();
    } catch (e) {
      for (const [pluginID, wasEnabled] of previous) {
        updateEnabled(pluginID, wasEnabled);
      }
      showToast((e as Error).message, "error");
    }
  }

  return (
    <div className="h-full overflow-y-auto bg-background">
      <div className="mx-auto max-w-3xl p-6 sm:p-8 lg:p-10 space-y-8">
        <SettingsPageHeader
          title="Sandbox"
          description="Select which sandbox backend agents use. Only one can be active at a time."
        />

        <div className="space-y-4">
          <div className="flex items-center gap-2 border-b border-border/40 pb-2">
            <Box className="size-4 shrink-0 text-muted-foreground/80" />
            <h4 className="text-xs font-semibold text-muted-foreground/85 uppercase tracking-wider">
              Sandbox Backends
            </h4>
            <Badge variant="secondary" className="text-[10px] py-0 px-1.5 rounded-md">
              {sandboxPlugins.length}
            </Badge>
          </div>

          <div className="space-y-4">
            {sandboxPlugins.map((p) => {
              const meta = sandboxMeta(p.id);
              return (
                <div
                  key={p.id}
                  className={`group relative flex flex-col justify-between rounded-2xl border bg-card p-5 transition-all hover:border-border/60 hover:shadow-xs ${
                    p.enabled ? "border-primary/45 shadow-xs" : "border-border/40"
                  }`}
                >
                  <div className="flex items-center justify-between gap-4">
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-2">
                        <span className="font-semibold text-sm">{pluginLabel(p)}</span>
                        {p.enabled && (
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
                        <p className="text-xs text-muted-foreground mt-1.5 leading-relaxed">
                          {pluginDescription(p)}
                        </p>
                      )}
                    </div>
                    <Switch
                      checked={p.enabled}
                      onCheckedChange={(checked) => void toggleSandbox(p.id, checked)}
                    />
                  </div>
                  {(meta.features.length > 0 || meta.limitations.length > 0) && (
                    <div className="mt-4 pt-4 border-t border-border/30 flex flex-col gap-4 sm:flex-row sm:gap-6">
                      {meta.features.length > 0 && (
                        <div className="flex-1">
                          <p className="text-[11px] font-semibold text-success-foreground mb-1.5 uppercase tracking-wide">
                            Features
                          </p>
                          <ul className="text-[11px] text-muted-foreground space-y-1">
                            {meta.features.map((f) => (
                              <li key={f} className="flex items-start gap-1">
                                <span className="text-success-foreground shrink-0 font-bold">
                                  ✓
                                </span>
                                <span>{f}</span>
                              </li>
                            ))}
                          </ul>
                        </div>
                      )}
                      {meta.limitations.length > 0 && (
                        <div className="flex-1">
                          <p className="text-[11px] font-semibold text-warning-foreground mb-1.5 uppercase tracking-wide">
                            Limitations
                          </p>
                          <ul className="text-[11px] text-muted-foreground space-y-1">
                            {meta.limitations.map((l) => (
                              <li key={l} className="flex items-start gap-1">
                                <span className="text-warning-foreground shrink-0 font-bold">
                                  ⚠
                                </span>
                                <span>{l}</span>
                              </li>
                            ))}
                          </ul>
                        </div>
                      )}
                    </div>
                  )}
                </div>
              );
            })}
            {sandboxPlugins.length === 0 && (
              <div className="text-center text-muted-foreground text-sm py-8 border border-dashed border-border/40 rounded-2xl bg-card/45">
                No sandbox plugins registered.
              </div>
            )}
          </div>
        </div>
      </div>
      <ToastContainer messages={toasts} />
    </div>
  );
}
