import { useCallback, useEffect, useState } from "react";
import { useNavigate, useParams } from "@tanstack/react-router";
import { listPlugins, togglePlugin as togglePluginRequest } from "@/lib/api-client/sdk.gen";
import type { Plugin } from "@/lib/types";
import { pluginLabel, pluginDescription, sandboxMeta } from "@/features/plugins/pluginUtils";
import { Badge } from "@/components/ui/badge";
import { Switch } from "@/components/ui/switch";
import { useI18n } from "@/lib/i18n";
import { useToast, ToastContainer } from "@/hooks/use-toast";
import { SettingsDetailLayout } from "@/features/settings/SettingsDetailLayout";
import { SettingsEmptyState } from "@/features/settings/SettingsEmptyState";
import {
  SettingsListHeader,
  SettingsListItem,
  SettingsListBody,
} from "@/features/settings/SettingsListPanel";
import { DetailPanel, DetailPanelHeader } from "@/features/settings/SettingsDetailPanel";

const validSandboxBackends = new Set(["sandbox/docker", "sandbox/local", "sandbox/none"]);

export function SandboxPage() {
  const { t } = useI18n();
  const navigate = useNavigate();
  const params = useParams({ strict: false }) as { backendId?: string };
  const backendId = params.backendId;

  const [plugins, setPlugins] = useState<Plugin[]>([]);
  const { toasts, showToast } = useToast(4000);

  const sandboxPlugins = plugins.filter(
    (p) => p.kind === "sandbox" && validSandboxBackends.has(p.id),
  );

  const selectedPlugin = backendId ? sandboxPlugins.find((p) => p.name === backendId) : undefined;

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

  let detail: React.ReactNode = undefined;
  if (selectedPlugin) {
    const meta = sandboxMeta(selectedPlugin.id);
    detail = (
      <DetailPanel>
        <DetailPanelHeader
          title={pluginLabel(selectedPlugin)}
          subtitle={
            <div className="flex items-center gap-1.5">
              {selectedPlugin.enabled && (
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
          }
          action={
            <Switch
              checked={selectedPlugin.enabled}
              onCheckedChange={(checked) => void toggleSandbox(selectedPlugin.id, checked)}
            />
          }
        />

        {pluginDescription(selectedPlugin) && (
          <p className="text-sm text-muted-foreground leading-relaxed">
            {pluginDescription(selectedPlugin)}
          </p>
        )}

        {meta.features.length > 0 && (
          <div>
            <p className="text-xs font-semibold text-muted-foreground mb-2">Features</p>
            <ul className="text-sm text-muted-foreground space-y-1.5">
              {meta.features.map((f) => (
                <li key={f} className="flex items-start gap-2">
                  <span className="text-success-foreground shrink-0 font-bold mt-0.5">✓</span>
                  <span>{f}</span>
                </li>
              ))}
            </ul>
          </div>
        )}

        {meta.limitations.length > 0 && (
          <div>
            <p className="text-xs font-semibold text-muted-foreground mb-2">Limitations</p>
            <ul className="text-sm text-muted-foreground space-y-1.5">
              {meta.limitations.map((l) => (
                <li key={l} className="flex items-start gap-2">
                  <span className="text-warning-foreground shrink-0 font-bold mt-0.5">⚠</span>
                  <span>{l}</span>
                </li>
              ))}
            </ul>
          </div>
        )}
      </DetailPanel>
    );
  }

  return (
    <>
      <SettingsDetailLayout
        list={
          <>
            <SettingsListHeader title={t("settings.nav.sandbox")} />
            <SettingsListBody>
              {sandboxPlugins.map((p) => (
                <SettingsListItem
                  key={p.id}
                  active={backendId === p.name}
                  onClick={() =>
                    void navigate({
                      to: "/settings/sandbox/$backendId",
                      params: { backendId: p.name },
                    })
                  }
                >
                  <div className="flex items-center gap-2">
                    <span
                      className={`shrink-0 size-1.5 rounded-full ${p.enabled ? "bg-green-500" : "bg-muted-foreground"}`}
                    />
                    <span className="text-sm truncate">{pluginLabel(p)}</span>
                  </div>
                  <span className="text-xs text-muted-foreground">
                    {p.enabled ? "active" : "disabled"}
                  </span>
                </SettingsListItem>
              ))}
            </SettingsListBody>
          </>
        }
        detail={detail}
        emptyState={
          <SettingsEmptyState
            message="No sandbox backends"
            description="Select a sandbox backend to configure."
          />
        }
        onBack={() => void navigate({ to: "/settings/sandbox" })}
      />
      <ToastContainer messages={toasts} />
    </>
  );
}
