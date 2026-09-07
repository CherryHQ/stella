import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Button } from "@/components/ui/button";
import { Sheet, SheetPopup } from "@/components/ui/sheet";
import { DetailPanel, DetailPanelHeader } from "@/features/settings/SettingsDetailPanel";
import { McpInstallSheet } from "@/features/mcp/McpInstallSheet";
import {
  McpServerFields,
  type McpAuthType,
  type McpTransport,
} from "@/features/mcp/McpServerFields";
import { getMcpServer, getPlugin, updateMcpServer } from "@/lib/api-client/sdk.gen";
import type { AgentMcpServer, PluginDefinition } from "@/lib/api-client/types.gen";
import { apiErrorMessage } from "@/lib/api-error";
import { useI18n } from "@/lib/i18n";

type Notify = (message: string, kind?: "success" | "error") => void;

export function AgentMcpServerSheet({
  agentId,
  isAdmin,
  open,
  server,
  formKey,
  onOpenChange,
  notify,
}: {
  agentId: string;
  isAdmin: boolean;
  open: boolean;
  server: AgentMcpServer | null;
  formKey: number;
  onOpenChange: (open: boolean) => void;
  notify: Notify;
}) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const childQuery = useQuery({
    queryKey: ["mcp-server", server?.config_id],
    enabled: open && !!server,
    queryFn: async () =>
      (await getMcpServer({ path: { id: server!.config_id }, throwOnError: true })).data,
  });
  const pluginQuery = useQuery({
    queryKey: ["plugin", server?.plugin_id],
    enabled: open && !!server,
    queryFn: async () =>
      (await getPlugin({ path: { plugin_id: server!.plugin_id }, throwOnError: true }))
        .data as PluginDefinition,
  });
  const [url, setURL] = useState("");
  const [transport, setTransport] = useState<McpTransport>("streamable_http");
  const [authType, setAuthType] = useState<McpAuthType>("none");
  const [credentialMode, setCredentialMode] = useState<"shared" | "per_user">("shared");
  const [token, setToken] = useState("");
  const [oauthClientId, setOauthClientId] = useState("");
  const [oauthClientSecret, setOauthClientSecret] = useState("");

  useEffect(() => {
    const child = childQuery.data;
    if (!child) return;
    setURL(child.url ?? "");
    setTransport(child.transport ?? "streamable_http");
    setAuthType(child.auth_type ?? "none");
    setCredentialMode(child.credential_mode ?? "shared");
    setToken("");
    setOauthClientId("");
    setOauthClientSecret("");
  }, [childQuery.data]);

  const updateMutation = useMutation({
    mutationFn: async () => {
      const child = childQuery.data;
      if (!server || !child) throw new Error("MCP child server is unavailable");
      const credentials: Record<string, string> = {};
      if (authType === "bearer" && token.trim()) credentials.token = token.trim();
      if (authType === "oauth") {
        if (oauthClientId.trim()) credentials.oauth_client_id = oauthClientId.trim();
        if (oauthClientSecret) credentials.oauth_client_secret = oauthClientSecret;
      }
      const { data } = await updateMcpServer({
        path: { id: server.config_id },
        body: {
          expected_parent_revision: child.parent_revision ?? server.parent_revision,
          url: url.trim() || undefined,
          transport,
          credential_mode: credentialMode,
          metadata: oauthClientId.trim()
            ? { oauth: { client_id: oauthClientId.trim() } }
            : undefined,
          credentials: Object.keys(credentials).length > 0 ? credentials : undefined,
        },
        throwOnError: true,
      });
      return data;
    },
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["agent-tools", agentId] }),
        queryClient.invalidateQueries({ queryKey: ["agent-mcp-servers", agentId] }),
        queryClient.invalidateQueries({ queryKey: ["mcp-server", server?.config_id] }),
      ]);
      notify(t("mcp.updated"), "success");
      onOpenChange(false);
    },
    onError: (error) => notify(apiErrorMessage(error, t("mcp.saveFailed")), "error"),
  });

  if (!server)
    return (
      <McpInstallSheet
        open={open}
        onOpenChange={onOpenChange}
        notify={notify}
        defaultScope="user_agent"
        agentId={agentId}
        isAdmin={isAdmin}
        key={formKey}
      />
    );
  const child = childQuery.data;
  const plugin = pluginQuery.data;
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetPopup side="right" className="w-full sm:w-[560px] sm:max-w-[560px]">
        <DetailPanel>
          <DetailPanelHeader title={plugin?.display_name ?? server.plugin_id} />
          {child && plugin ? (
            <div className="space-y-4">
              <McpServerFields
                name={plugin.display_name}
                onNameChange={() => undefined}
                url={url}
                onUrlChange={setURL}
                transport={transport}
                onTransportChange={setTransport}
                authType={authType}
                onAuthTypeChange={setAuthType}
                token={token}
                onTokenChange={setToken}
                editing
                oauthClientId={oauthClientId}
                onOauthClientIdChange={setOauthClientId}
                oauthClientSecret={oauthClientSecret}
                onOauthClientSecretChange={setOauthClientSecret}
                credentialMode={credentialMode}
                onCredentialModeChange={setCredentialMode}
                showCredentialMode
                showName={false}
              />
              <div className="flex justify-end gap-2">
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => onOpenChange(false)}
                  disabled={updateMutation.isPending}
                >
                  {t("common.cancel")}
                </Button>
                <Button
                  size="sm"
                  onClick={() => updateMutation.mutate()}
                  loading={updateMutation.isPending}
                >
                  {t("common.save")}
                </Button>
              </div>
            </div>
          ) : (
            <p className="text-sm text-muted-foreground">
              {childQuery.isError || pluginQuery.isError
                ? t("plugins.scopeUnavailable")
                : t("agents.tools.loading")}
            </p>
          )}
        </DetailPanel>
      </SheetPopup>
    </Sheet>
  );
}
