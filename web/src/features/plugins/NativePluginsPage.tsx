import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  allowNativePluginAgent,
  createNativePluginAgentDeny,
  updateNativePlugin,
} from "@/lib/api-client/sdk.gen";
import type { NativePlugin } from "@/lib/api-client";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Field, FieldLabel } from "@/components/ui/field";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Select,
  SelectItem,
  SelectPopup,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";
import { ErrorState } from "@/components/RouteFallback";
import { SettingsEmptyState } from "@/features/settings/SettingsEmptyState";
import {
  SettingsGridPage,
  SettingsList,
  SettingsRow,
  SettingsSection,
} from "@/features/settings/SettingsCardGrid";
import { useToast } from "@/hooks/use-toast";
import { apiErrorMessage } from "@/lib/api-error";
import { useI18n } from "@/lib/i18n";
import { allAgentsAdminQueryOptions } from "@/lib/queries/agents";
import { nativePluginDenialsQueryOptions, nativePluginsQueryOptions } from "@/lib/queries/plugins";
import { ShieldCheck } from "lucide-react";

function nativePath(id: string): { kind: string; name: string } {
  const slash = id.indexOf("/");
  return slash < 0
    ? { kind: "", name: id }
    : { kind: id.slice(0, slash), name: id.slice(slash + 1) };
}

export function NativePluginsPage() {
  const { t } = useI18n();
  const { showToast } = useToast();
  const queryClient = useQueryClient();
  const pluginsQuery = useQuery(nativePluginsQueryOptions);
  const [selectedID, setSelectedID] = useState("");
  const plugins = pluginsQuery.data ?? [];
  const nativeItems = plugins.map((plugin) => ({ value: plugin.id, label: plugin.id }));
  const selected = plugins.find((plugin) => plugin.id === selectedID) ?? plugins[0];
  const denialsQuery = useQuery(nativePluginDenialsQueryOptions(selected?.id ?? "", !!selected));
  const agentsQuery = useQuery(allAgentsAdminQueryOptions(!!selected));
  const deniedIDs = useMemo(
    () => new Set((denialsQuery.data ?? []).map((denial) => denial.agent_id)),
    [denialsQuery.data],
  );

  useEffect(() => {
    if (!selectedID && plugins[0]) setSelectedID(plugins[0].id);
    if (selectedID && !plugins.some((plugin) => plugin.id === selectedID)) setSelectedID("");
  }, [plugins, selectedID]);

  const invalidateNative = (nativeID: string) => {
    void queryClient.invalidateQueries({ queryKey: ["native-plugins"] });
    void queryClient.invalidateQueries({
      queryKey: ["native-plugin-denials", nativeID],
    });
  };
  const updateMutation = useMutation({
    mutationFn: async ({ plugin, enabled }: { plugin: NativePlugin; enabled: boolean }) => {
      const path = nativePath(plugin.id);
      return updateNativePlugin({
        path,
        body: { is_enabled: enabled },
        throwOnError: true,
      });
    },
    onSuccess: () => showToast(t("nativePlugins.updated")),
    onSettled: (_data, _error, variables) => invalidateNative(variables.plugin.id),
    onError: (error) => showToast(apiErrorMessage(error, t("nativePlugins.updateFailed")), "error"),
  });
  const denyMutation = useMutation({
    mutationFn: async ({ nativeID, agentID }: { nativeID: string; agentID: string }) =>
      createNativePluginAgentDeny({
        path: nativePath(nativeID),
        body: { agent_id: agentID },
        throwOnError: true,
      }),
    onSuccess: () => showToast(t("nativePlugins.agentDenied")),
    onSettled: (_data, _error, variables) => invalidateNative(variables.nativeID),
    onError: (error) => showToast(apiErrorMessage(error, t("nativePlugins.updateFailed")), "error"),
  });
  const allowMutation = useMutation({
    mutationFn: async ({ nativeID, agentID }: { nativeID: string; agentID: string }) =>
      allowNativePluginAgent({
        path: { ...nativePath(nativeID), agent_id: agentID },
        throwOnError: true,
      }),
    onSuccess: () => showToast(t("nativePlugins.agentAllowed")),
    onSettled: (_data, _error, variables) => invalidateNative(variables.nativeID),
    onError: (error) => showToast(apiErrorMessage(error, t("nativePlugins.updateFailed")), "error"),
  });

  if (pluginsQuery.isPending) {
    return (
      <div className="flex h-full items-center justify-center">
        <Spinner className="size-5" />
      </div>
    );
  }
  if (pluginsQuery.isError) {
    return (
      <ErrorState
        title={t("nativePlugins.loadFailed")}
        description={apiErrorMessage(pluginsQuery.error, t("common.error"))}
        onRetry={() => void pluginsQuery.refetch()}
      />
    );
  }

  return (
    <SettingsGridPage title={t("nativePlugins.title")}>
      {plugins.length === 0 ? (
        <SettingsEmptyState
          icon={<ShieldCheck size={20} />}
          message={t("nativePlugins.noPlugins")}
          description={t("nativePlugins.noPluginsDesc")}
        />
      ) : (
        <SettingsSection
          icon={<ShieldCheck size={16} />}
          title={t("nativePlugins.globalTitle")}
          description={t("nativePlugins.globalDescription")}
          count={plugins.length}
        >
          <SettingsList>
            {plugins.map((plugin) => (
              <SettingsRow
                key={plugin.id}
                title={plugin.id}
                status={
                  <Badge variant={plugin.is_enabled ? "success" : "secondary"} size="sm">
                    {plugin.is_enabled ? t("nativePlugins.enabled") : t("nativePlugins.disabled")}
                  </Badge>
                }
                primary={
                  <Switch
                    checked={plugin.is_enabled}
                    disabled={updateMutation.isPending}
                    onCheckedChange={(enabled) => updateMutation.mutate({ plugin, enabled })}
                    aria-label={plugin.id}
                  />
                }
              />
            ))}
          </SettingsList>
        </SettingsSection>
      )}

      {plugins.length > 0 && selected && (
        <SettingsSection
          icon={<ShieldCheck size={16} />}
          title={t("nativePlugins.agentTitle")}
          description={t("nativePlugins.agentDescription")}
        >
          <Card>
            <CardHeader>
              <CardTitle>{t("nativePlugins.selectedPlugin")}</CardTitle>
              <CardDescription>{t("nativePlugins.selectedPluginDesc")}</CardDescription>
            </CardHeader>
            <CardContent className="space-y-4">
              <Field>
                <FieldLabel>{t("nativePlugins.selectedPlugin")}</FieldLabel>
                <Select
                  items={nativeItems}
                  value={selected.id}
                  disabled={denyMutation.isPending || allowMutation.isPending}
                  onValueChange={(value) => setSelectedID(value ?? "")}
                >
                  <SelectTrigger aria-label={t("nativePlugins.selectedPlugin")}>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectPopup>
                    {plugins.map((plugin) => (
                      <SelectItem key={plugin.id} value={plugin.id}>
                        {plugin.id}
                      </SelectItem>
                    ))}
                  </SelectPopup>
                </Select>
              </Field>
              {denialsQuery.isPending || agentsQuery.isPending ? (
                <Spinner className="size-5" />
              ) : denialsQuery.isError || agentsQuery.isError ? (
                <ErrorState
                  title={t("nativePlugins.agentLoadFailed")}
                  description={t("common.error")}
                  onRetry={() => {
                    void denialsQuery.refetch();
                    void agentsQuery.refetch();
                  }}
                />
              ) : (agentsQuery.data ?? []).length === 0 ? (
                <SettingsEmptyState
                  icon={<ShieldCheck size={20} />}
                  message={t("nativePlugins.noAgents")}
                  description={t("nativePlugins.noAgentsDesc")}
                />
              ) : (
                <div className="space-y-2">
                  {(agentsQuery.data ?? []).map((agent) => {
                    const agentID = agent.id ?? "";
                    if (!agentID) return null;
                    const denied = deniedIDs.has(agentID);
                    return (
                      <SettingsRow
                        key={agentID}
                        title={agent.name || agentID}
                        subtitle={agentID}
                        primary={
                          <Button
                            type="button"
                            variant={denied ? "outline" : "secondary"}
                            size="xs"
                            disabled={denyMutation.isPending || allowMutation.isPending}
                            onClick={() =>
                              denied
                                ? allowMutation.mutate({ nativeID: selected.id, agentID })
                                : denyMutation.mutate({ nativeID: selected.id, agentID })
                            }
                          >
                            {denied ? t("nativePlugins.allow") : t("nativePlugins.deny")}
                          </Button>
                        }
                      />
                    );
                  })}
                </div>
              )}
            </CardContent>
          </Card>
        </SettingsSection>
      )}
    </SettingsGridPage>
  );
}
