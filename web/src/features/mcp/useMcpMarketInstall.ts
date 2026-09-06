import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { createPlugin, startPluginConfigOAuth } from "@/lib/api-client/sdk.gen";
import type { McpRegistryServer, PluginConfig } from "@/lib/api-client/types.gen";
import type { InstallRequest, WritableScope } from "@/features/marketplace/InstallScopeStep";
import { apiErrorMessage } from "@/lib/api-error";

// One marketplace install creates a first-party MCP plugin and its initial
// config. Secret-bearing registry entries fail explicitly until the secure
// mutation seam is available; no plaintext credential is sent here.
export type InstallArgs = {
  server: McpRegistryServer;
  scope: WritableScope;
  agentId?: string;
  bearerSecret?: string;
};

// The notify/t callbacks come from the host sheet; call sites only pass
// MessageKey literals, so the translator type is imported rather than re-derived.
import type { useI18n } from "@/lib/i18n";

export function useMcpMarketInstall(
  notify: (message: string, kind?: "success" | "error") => void,
  t: ReturnType<typeof useI18n>["t"],
) {
  const queryClient = useQueryClient();
  const [created, setCreated] = useState<PluginConfig | null>(null);

  const mutation = useMutation({
    mutationFn: async ({ server, scope, agentId, bearerSecret }: InstallArgs) => {
      if (server.auth === "bearer" && bearerSecret?.trim()) {
        // The unified write contract has no plaintext credential field. Keep
        // this explicit failure instead of claiming an install succeeded.
        throw new Error("secure credential writes are unavailable");
      }
      const { data } = await createPlugin({
        body: {
          display_name: server.name,
          namespace: server.id,
          backend: "mcp",
          definition_spec: {},
          initial_config: {
            scope,
            ...(agentId ? { agent_id: agentId } : {}),
            config: {
              url: server.url,
              transport: "streamable_http",
              auth_type: server.auth === "bearer" ? "bearer" : "none",
              credential_mode: "shared",
            },
          },
        },
        throwOnError: true,
      });
      return data?.config;
    },
    onSuccess: (data) => {
      setCreated(data ?? null);
      void queryClient.invalidateQueries({ queryKey: ["agent-mcp-servers"] });
      void queryClient.invalidateQueries({ queryKey: ["mcp-servers"] });
    },
    onError: (e) => notify(apiErrorMessage(e, t("mcp.saveFailed")), "error"),
  });

  // Nested OAuth is the only connection action. It is intentionally separate
  // from config writes so the callback can enforce common plugin visibility.
  const connect = useMutation({
    mutationFn: async (config: PluginConfig) => {
      const [kind, ...nameParts] = config.plugin_id.split("/");
      if (!kind || nameParts.length === 0) throw new Error("invalid plugin id");
      const { data } = await startPluginConfigOAuth({
        path: { kind, name: nameParts.join("/"), config_id: config.id },
        throwOnError: true,
      });
      return data?.authorization_url ?? "";
    },
    onSuccess: (url) => {
      if (url) window.location.href = url;
    },
    onError: (e) => notify(apiErrorMessage(e, t("mcp.connectFailed")), "error"),
  });

  return { mutation, created, setCreated, connect, connectPending: connect.isPending };
}

/** Builds the deferred install request handed to the shared scope step. */
export function buildInstallRequest(
  server: McpRegistryServer,
  run: (args: InstallArgs) => Promise<unknown>,
  confirmLabel: string,
  agentId?: string,
  bearerSecret?: string,
): InstallRequest<WritableScope> {
  return {
    name: server.name,
    confirmLabel,
    run: async (scope) => {
      // The mutation reports its own failure; a rejected run keeps the step open.
      try {
        await run({ server, scope, agentId, bearerSecret });
        return true;
      } catch {
        return false;
      }
    },
  };
}
