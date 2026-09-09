import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import {
  createMcpServer,
  probeMcpServer,
  startMcpServerOAuth,
  updateMcpServerCredentials,
} from "@/lib/api-client/sdk.gen";
import type {
  CreateMcpServerRequest,
  McpRegistryServer,
  McpServer,
} from "@/lib/api-client/types.gen";
import type { InstallRequest, WritableScope } from "@/features/marketplace/InstallScopeStep";
import { apiErrorMessage } from "@/lib/api-error";
import type { useI18n } from "@/lib/i18n";
import { ensureMcpBearerCredentialRef } from "./mcp-credential";

export type InstallArgs = {
  server: McpRegistryServer;
  scope: WritableScope;
  agentId?: string;
  bearerSecret?: string;
};
export function registryPluginID(id: string): string {
  const value = id
    .toLowerCase()
    .replace(/[^a-z0-9-]+/g, "-")
    .replace(/-{2,}/g, "-")
    .replace(/^-+|-+$/g, "");
  if (!value) throw new Error("registry server id cannot produce a valid name");
  return value;
}

export function useMcpMarketInstall(
  notify: (message: string, kind?: "success" | "error") => void,
  t: ReturnType<typeof useI18n>["t"],
) {
  const queryClient = useQueryClient();
  const [created, setCreated] = useState<McpServer | null>(null);
  const mutation = useMutation({
    mutationFn: async ({ server, scope, agentId, bearerSecret }: InstallArgs) => {
      const declaration: CreateMcpServerRequest["declaration"] = {
        url: server.url,
        transport: server.transport,
        auth_type: server.auth === "bearer" ? "bearer" : "none",
        credential_mode: scope === "system" || scope === "system_agent" ? "shared" : "per_user",
      };
      if (server.auth === "bearer") declaration.credential_ref = ensureMcpBearerCredentialRef();
      const body: CreateMcpServerRequest = {
        name: registryPluginID(server.id),
        scope,
        declaration,
      };
      if (agentId) body.agent_id = agentId;
      const { data } = await createMcpServer({
        body,
        throwOnError: true,
      });
      if (!data) throw new Error("MCP server was not returned");
      let current = data;
      if (server.auth === "bearer" && bearerSecret?.trim()) {
        const result = await updateMcpServerCredentials({
          path: { id: data.id },
          body: {
            expected_digest: data.content_digest,
            bearer_token: bearerSecret.trim(),
          },
          throwOnError: true,
        });
        current = result.data ?? current;
      }
      await probeMcpServer({ path: { id: current.id }, throwOnError: true });
      return current;
    },
    onSuccess: (data) => {
      setCreated(data ?? null);
      void queryClient.invalidateQueries({ queryKey: ["mcp-servers"] });
      void queryClient.invalidateQueries({ queryKey: ["agent-mcp-servers"] });
    },
    onError: (error) => notify(apiErrorMessage(error, t("mcp.saveFailed")), "error"),
  });
  const connect = useMutation({
    mutationFn: async (server: McpServer) =>
      (
        await startMcpServerOAuth({
          path: { id: server.id },
          body: { expected_digest: server.content_digest },
          throwOnError: true,
        })
      ).data?.authorization_url ?? "",
    onSuccess: (url) => {
      if (url) window.location.href = url;
    },
    onError: (error) => notify(apiErrorMessage(error, t("mcp.connectFailed")), "error"),
  });
  return {
    mutation,
    created,
    setCreated,
    connect,
    connectPending: connect.isPending,
  };
}

export function buildInstallRequest(
  server: McpRegistryServer,
  run: (args: InstallArgs) => Promise<McpServer>,
  confirmLabel: string,
  agentId?: string,
  bearerSecret?: string,
): InstallRequest<WritableScope> {
  return {
    name: server.name,
    confirmLabel,
    run: async (scope) => {
      try {
        await run({ server, scope, agentId, bearerSecret });
        return true;
      } catch {
        return false;
      }
    },
  };
}
