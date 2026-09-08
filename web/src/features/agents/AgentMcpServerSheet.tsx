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
import {
  getMcpServer,
  updateMcpServer,
  updateMcpServerCredentials,
} from "@/lib/api-client/sdk.gen";
import type { AgentMcpServer } from "@/lib/api-client/types.gen";
import { apiErrorMessage } from "@/lib/api-error";
import { useI18n } from "@/lib/i18n";
import { transitionMcpAuthType } from "@/features/mcp/mcp-credential";

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
    queryKey: ["mcp-server", server?.id],
    enabled: open && !!server,
    queryFn: async () =>
      (
        await getMcpServer({
          path: { id: server!.id },
          throwOnError: true,
        })
      ).data,
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
    const declaration = child.declaration;
    setURL(declaration?.url ?? "");
    setTransport(declaration?.transport ?? "streamable_http");
    setAuthType(declaration?.auth_type ?? "none");
    setCredentialMode(declaration?.credential_mode ?? "shared");
    setToken("");
    setOauthClientId("");
    setOauthClientSecret("");
  }, [childQuery.data]);

  const updateMutation = useMutation({
    mutationFn: async () => {
      const child = childQuery.data;
      if (!server || !child || !child.declaration)
        throw new Error("MCP child server is unavailable");
      const declaration = {
        ...transitionMcpAuthType(child.declaration, authType),
        url: url.trim(),
        transport,
        credential_mode: credentialMode,
        ...(authType === "oauth" && oauthClientId.trim()
          ? { client_id: oauthClientId.trim() }
          : {}),
      };
      const { data } = await updateMcpServer({
        path: { id: server.id },
        body: {
          declaration,
          expected_digest: child.content_digest,
        },
        throwOnError: true,
      });
      if (authType === "bearer" && token.trim()) {
        await updateMcpServerCredentials({
          path: { id: server.id },
          body: {
            expected_digest: data.content_digest,
            bearer_token: token.trim(),
          },
          throwOnError: true,
        });
      } else if (authType === "oauth" && oauthClientSecret) {
        await updateMcpServerCredentials({
          path: { id: server.id },
          body: {
            expected_digest: data.content_digest,
            client_secret: oauthClientSecret,
          },
          throwOnError: true,
        });
      }
      return data;
    },
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["agent-tools", agentId] }),
        queryClient.invalidateQueries({
          queryKey: ["agent-mcp-servers", agentId],
        }),
        queryClient.invalidateQueries({
          queryKey: ["mcp-server", server?.id],
        }),
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
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetPopup side="right" className="w-full sm:w-[560px] sm:max-w-[560px]">
        <DetailPanel>
          <DetailPanelHeader title={server.name} />
          {child ? (
            <div className="space-y-4">
              <McpServerFields
                name={server.name}
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
              {childQuery.isError ? t("plugins.scopeUnavailable") : t("agents.tools.loading")}
            </p>
          )}
        </DetailPanel>
      </SheetPopup>
    </Sheet>
  );
}
