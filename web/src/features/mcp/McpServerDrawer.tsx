import { useMutation, useQueryClient } from "@tanstack/react-query";
import { RefreshCw } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { OAuthConnectionActions } from "@/components/OAuthConnectionActions";
import {
  Drawer,
  DrawerClose,
  DrawerHeader,
  DrawerPopup,
  DrawerTitle,
} from "@/components/ui/drawer";
import { probeMcpServer } from "@/lib/api-client/sdk.gen";
import type { McpServer } from "@/lib/api-client/types.gen";
import { apiErrorMessage } from "@/lib/api-error";
import { useI18n } from "@/lib/i18n";
import { SCOPE_LABEL_KEY } from "@/lib/skill-scope";
import { formatTime } from "@/lib/time";

function statusBadgeVariant(status: string) {
  if (status === "ok") return "success";
  if (status === "error" || status === "needs_auth") return "warning";
  return "outline";
}

/**
 * The read-side server drawer: health (status badge, last error, probed time)
 * with a Probe action, OAuth connect controls, and the persisted tool catalog.
 * Edit and delete stay on the host page — the drawer only reads and probes.
 */
export function McpServerDrawer({
  server,
  open,
  onOpenChange,
  onConnect,
  onDisconnect,
  onEdit,
  onDelete,
  notify,
}: {
  server: McpServer | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onConnect: (server: McpServer) => void;
  onDisconnect: (server: McpServer) => void;
  onEdit: (server: McpServer) => void;
  onDelete: (server: McpServer) => void;
  notify: (message: string, kind?: "success" | "error") => void;
}) {
  const { t } = useI18n();
  const queryClient = useQueryClient();

  const probe = useMutation({
    mutationFn: (target: McpServer) =>
      probeMcpServer({
        path: { id: target.id },
        query: {
          scope: target.scope,
          agent_id:
            target.scope === "user_agent" || target.scope === "system_agent"
              ? target.agent_id
              : undefined,
        },
        throwOnError: true,
      }),
    onSuccess: async ({ data }) => {
      notify(
        t("mcp.server.probed", { time: data?.probed_at ? formatTime(data.probed_at) : "—" }),
        "success",
      );
      await queryClient.invalidateQueries({ queryKey: ["mcp-servers"] });
      await queryClient.invalidateQueries({ queryKey: ["agent-mcp-servers"] });
    },
    onError: (e) => notify(apiErrorMessage(e, t("mcp.saveFailed")), "error"),
  });

  if (!server) return null;
  return (
    <Drawer open={open} onOpenChange={onOpenChange} position="right">
      <DrawerPopup position="right" className="w-full sm:w-[480px] sm:max-w-[480px]">
        <DrawerHeader>
          <DrawerTitle className="min-w-0 truncate font-mono">{server.name}</DrawerTitle>
          <DrawerClose aria-label={t("common.close")} />
        </DrawerHeader>
        <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto p-5">
          <div className="flex flex-wrap items-center gap-2">
            <Badge variant="outline" size="sm">
              {t(SCOPE_LABEL_KEY[server.scope])}
            </Badge>
            <Badge variant={statusBadgeVariant(server.status)} size="sm">
              {t(
                server.status === "ok"
                  ? "mcp.status.ok"
                  : server.status === "error"
                    ? "mcp.status.error"
                    : server.status === "needs_auth"
                      ? "mcp.status.needs_auth"
                      : "mcp.status.unknown",
              )}
            </Badge>
            <span className="text-xs text-muted-foreground">
              {server.probed_at
                ? t("mcp.server.probed", { time: formatTime(server.probed_at) })
                : t("mcp.server.neverProbed")}
            </span>
          </div>

          {server.status_error && (
            <div className="space-y-1">
              <p className="text-xs font-medium text-muted-foreground">
                {t("mcp.server.lastError")}
              </p>
              <p className="text-sm">{server.status_error}</p>
            </div>
          )}

          <div className="flex flex-wrap items-center gap-2">
            <Button
              variant="outline"
              size="sm"
              loading={probe.isPending}
              onClick={() => probe.mutate(server)}
            >
              <RefreshCw size={16} />
              {t("mcp.server.probe")}
            </Button>
            {server.auth_type === "oauth" && (
              <OAuthConnectionActions
                connected={server.oauth?.connected ?? false}
                needsReconnect={Boolean(server.oauth?.client_registered && !server.oauth.connected)}
                connectLabel={t("mcp.connect")}
                reconnectLabel={t("mcp.reconnect")}
                disconnectLabel={t("mcp.disconnect")}
                onConnect={() => onConnect(server)}
                onDisconnect={() => onDisconnect(server)}
              />
            )}
            <Button variant="ghost" size="sm" onClick={() => onEdit(server)}>
              {t("common.edit")}
            </Button>
            <Button variant="ghost" size="sm" onClick={() => onDelete(server)}>
              {t("common.delete")}
            </Button>
          </div>

          <div className="space-y-2">
            <p className="text-xs font-medium text-muted-foreground">{t("mcp.server.tools")}</p>
            {(server.tools ?? []).length === 0 ? (
              <p className="text-sm text-muted-foreground">{t("mcp.server.noTools")}</p>
            ) : (
              (server.tools ?? []).map((tool) => (
                <div key={tool.name} className="rounded-lg border p-3">
                  <p className="truncate font-mono text-sm font-medium">{tool.name}</p>
                  {tool.description && (
                    <p className="mt-1 text-xs text-muted-foreground">{tool.description}</p>
                  )}
                </div>
              ))
            )}
          </div>
        </div>
      </DrawerPopup>
    </Drawer>
  );
}
