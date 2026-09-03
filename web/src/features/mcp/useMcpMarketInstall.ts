import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import {
  createScopedMcpServer,
  startMcpoAuth,
  updateScopedMcpServer,
} from "@/lib/api-client/sdk.gen";
import type { McpRegistryServer, McpServer } from "@/lib/api-client/types.gen";
import type { InstallRequest, WritableScope } from "@/features/marketplace/InstallScopeStep";
import { apiErrorMessage } from "@/lib/api-error";

// One marketplace install: create the registration with registry provenance
// (and the bearer secret when the entry's header template asks for one), then
// report how the automatic probe landed so the UI can offer Connect.
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
  const [created, setCreated] = useState<McpServer | null>(null);

  const mutation = useMutation({
    mutationFn: async ({ server, scope, agentId, bearerSecret }: InstallArgs) => {
      const { data } = await createScopedMcpServer({
        body: {
          scope,
          agent_id: agentId,
          name: server.name,
          url: server.url,
          transport: "streamable_http",
          auth_type: server.auth === "bearer" ? "bearer" : "none",
          token: server.auth === "bearer" ? bearerSecret : undefined,
          source: "official",
          source_id: server.id,
          source_version: server.version ?? undefined,
        },
        throwOnError: true,
      });
      return data;
    },
    onSuccess: (data) => {
      setCreated(data);
      void queryClient.invalidateQueries({ queryKey: ["agent-mcp-servers"] });
      void queryClient.invalidateQueries({ queryKey: ["mcp-servers"] });
    },
    onError: (e) => notify(apiErrorMessage(e, t("mcp.saveFailed")), "error"),
  });

  // Connect: an OAuth-protected install lands as needs_auth with auth_type
  // none; switch it to oauth and jump straight into the authorization flow.
  const connect = useMutation({
    mutationFn: async (server: McpServer) => {
      await updateScopedMcpServer({
        path: { id: server.id },
        query: {
          scope: server.scope,
          agent_id:
            server.scope === "user_agent" || server.scope === "system_agent"
              ? server.agent_id
              : undefined,
        },
        body: { auth_type: "oauth" },
        throwOnError: true,
      });
      const { data } = await startMcpoAuth({
        path: { id: server.id },
        query: {
          scope: server.scope,
          agent_id:
            server.scope === "user_agent" || server.scope === "system_agent"
              ? server.agent_id
              : undefined,
        },
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
