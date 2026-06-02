import { useCallback, useEffect, useState } from "react";
import { listPlugins, togglePlugin as togglePluginRequest } from "@/lib/api-client/sdk.gen";
import type { Plugin } from "@/lib/types";
import { pluginDescription, pluginLabel, sandboxMeta } from "@/features/plugins/pluginUtils";
import { Badge } from "@/components/ui/badge";
import { Switch } from "@/components/ui/switch";
import { useToast, ToastContainer } from "@/hooks/use-toast";

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
    <div className="h-full">
      <div className="mx-auto max-w-3xl p-6">
        <h2 className="text-lg font-semibold mb-1">Sandbox</h2>
        <p className="text-xs text-muted-foreground mb-4">
          Select which sandbox backend agents use. Only one can be active at a time.
        </p>
        <div className="border border-border rounded-lg divide-y divide-border">
          {sandboxPlugins.map((p) => {
            const meta = sandboxMeta(p.id);
            return (
              <div key={p.id} className={`px-4 py-4${p.enabled ? " bg-muted/50" : ""}`}>
                <div className="flex items-center justify-between gap-4">
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-2">
                      <span className="font-medium text-sm">{pluginLabel(p)}</span>
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
                      <p className="text-xs text-muted-foreground mt-1 leading-relaxed">
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
                  <div className="mt-3 flex flex-col gap-2 sm:flex-row sm:gap-6">
                    {meta.features.length > 0 && (
                      <div className="flex-1">
                        <p className="text-[11px] font-medium text-success-foreground mb-1">
                          Features
                        </p>
                        <ul className="text-[11px] text-muted-foreground space-y-0.5">
                          {meta.features.map((f) => (
                            <li key={f} className="flex items-start gap-1">
                              <span className="text-success-foreground shrink-0">✓</span>
                              <span>{f}</span>
                            </li>
                          ))}
                        </ul>
                      </div>
                    )}
                    {meta.limitations.length > 0 && (
                      <div className="flex-1">
                        <p className="text-[11px] font-medium text-warning-foreground mb-1">
                          Limitations
                        </p>
                        <ul className="text-[11px] text-muted-foreground space-y-0.5">
                          {meta.limitations.map((l) => (
                            <li key={l} className="flex items-start gap-1">
                              <span className="text-warning-foreground shrink-0">⚠</span>
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
            <div className="px-4 py-8 text-center text-muted-foreground text-sm">
              No sandbox plugins registered.
            </div>
          )}
        </div>
      </div>
      <ToastContainer messages={toasts} />
    </div>
  );
}
