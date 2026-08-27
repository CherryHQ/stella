import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { X } from "lucide-react";
import { ScopeConfirmStep } from "@/components/ScopeConfirmStep";
import { Button } from "@/components/ui/button";
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field";
import { Sheet, SheetPopup } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import {
  McpServerFields,
  type McpAuthType,
  type McpTransport,
} from "@/features/mcp/McpServerFields";
import { createScopedMcpServer, updateScopedMcpServer } from "@/lib/api-client/sdk.gen";
import type { McpServer } from "@/lib/api-client/types.gen";
import { apiErrorMessage } from "@/lib/api-error";
import { useI18n } from "@/lib/i18n";
import type { MessageKey } from "@/lib/i18n/messages";
import { SCOPE_LABEL_KEY } from "@/lib/skill-scope";

type McpScope = McpServer["scope"];
type Notify = (message: string, kind?: "success" | "error") => void;

const INSTALL_SCOPES: McpScope[] = ["user", "user_agent", "system", "system_agent"];
const ADMIN_SCOPES = new Set<McpScope>(["system", "system_agent"]);

// The scope labels are the shared owner·range vocabulary (`skill-scope`), but a
// server is not a skill: the descriptions say what a scope means for a
// registration, so the radio list never explains an MCP server in skill words.
const SCOPE_DESC_KEY = {
  user: "mcp.scope.user.desc",
  user_agent: "mcp.scope.userAgent.desc",
  system: "mcp.scope.system.desc",
  system_agent: "mcp.scope.systemAgent.desc",
} satisfies Record<McpScope, MessageKey>;

function isAgentScope(scope: McpScope) {
  return scope === "user_agent" || scope === "system_agent";
}

/**
 * Add or edit one MCP server for a single agent. The profile owns this agent's
 * registrations. Personal inventory lives at `/settings/mcp`; deployment-owned
 * inventory lives at `/admin/resources/mcp`. The destination is chosen once at
 * create time and shown read-only afterwards.
 *
 * `formKey` remounts the draft: the caller bumps it every time the sheet opens,
 * so a cancelled edit never leaks its fields into the next one.
 */
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
  /** The server being edited, or null to create a new one. */
  server: McpServer | null;
  formKey: number;
  onOpenChange: (open: boolean) => void;
  notify: Notify;
}) {
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetPopup
        side="right"
        showCloseButton={false}
        className="w-full sm:w-[560px] sm:max-w-[560px]"
      >
        <ServerForm
          key={formKey}
          agentId={agentId}
          isAdmin={isAdmin}
          server={server}
          notify={notify}
          onDone={() => onOpenChange(false)}
        />
      </SheetPopup>
    </Sheet>
  );
}

function ServerForm({
  agentId,
  isAdmin,
  server,
  notify,
  onDone,
}: {
  agentId: string;
  isAdmin: boolean;
  server: McpServer | null;
  notify: Notify;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const editing = !!server;

  const [scope, setScope] = useState<McpScope>(server?.scope ?? "user_agent");
  // Creating asks for the destination in a second step, the same way a skill
  // install does: the choice decides who else gets this server, so it is a
  // deliberate confirmation rather than a field passed on the way down.
  const [confirming, setConfirming] = useState(false);
  const [name, setName] = useState(server?.name ?? "");
  const [url, setUrl] = useState(server?.url ?? "");
  const [transport, setTransport] = useState<McpTransport>(server?.transport ?? "streamable_http");
  const [authType, setAuthType] = useState<McpAuthType>(server?.auth_type ?? "none");
  // Never prefilled: the vault stores the token encrypted and never returns it.
  const [token, setToken] = useState("");
  const [enabled, setEnabled] = useState(server?.enabled ?? true);

  const scopes = INSTALL_SCOPES.filter((s) => isAdmin || !ADMIN_SCOPES.has(s));

  const save = useMutation({
    mutationFn: async () => {
      if (server) {
        await updateScopedMcpServer({
          path: { id: server.id },
          query: {
            scope: server.scope,
            agent_id: isAgentScope(server.scope) ? server.agent_id : undefined,
          },
          // No scope in the body on purpose: an edit here never moves a server
          // between scopes, so the backend keeps the one it was addressed with.
          body: {
            name: name.trim(),
            url: url.trim(),
            transport,
            auth_type: authType,
            enabled,
            token: authType === "bearer" && token.trim() ? token : undefined,
          },
          throwOnError: true,
        });
        return;
      }
      await createScopedMcpServer({
        body: {
          scope,
          agent_id: isAgentScope(scope) ? agentId : undefined,
          name: name.trim(),
          url: url.trim(),
          transport,
          auth_type: authType,
          token: authType === "bearer" ? token : undefined,
        },
        throwOnError: true,
      });
    },
    onSuccess: async () => {
      notify(editing ? t("mcp.updated") : t("mcp.created"), "success");
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["agent-tools", agentId] }),
        queryClient.invalidateQueries({ queryKey: ["agent-mcp-servers", agentId] }),
      ]);
      onDone();
    },
    // Stay on the step on failure — the toast already said why, and the chosen
    // destination is still there to retry with.
    onError: (error) => notify(apiErrorMessage(error, t("mcp.saveFailed")), "error"),
  });

  const tokenReady = authType !== "bearer" || editing || token.trim() !== "";
  const canSave = name.trim() !== "" && url.trim() !== "" && tokenReady;

  return (
    <div className="relative flex h-full min-h-0 flex-col">
      <div className="flex items-center gap-3 border-b p-5">
        <h2 className="min-w-0 flex-1 truncate text-base font-semibold">
          {editing ? t("mcp.editTitle") : t("mcp.addTitle")}
        </h2>
        <Button size="icon-sm" variant="ghost" aria-label={t("common.close")} onClick={onDone}>
          <X size={16} />
        </Button>
      </div>

      <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto p-5">
        {/* Editing never moves a server between scopes — that is the settings
            page's job — so its destination is stated, not offered. */}
        {editing && server && (
          <Field>
            <FieldLabel>{t("mcp.scope")}</FieldLabel>
            <p className="text-sm">{t(SCOPE_LABEL_KEY[server.scope])}</p>
            <FieldDescription>{t("mcp.scope.lockedDescription")}</FieldDescription>
          </Field>
        )}

        <McpServerFields
          name={name}
          onNameChange={setName}
          url={url}
          onUrlChange={setUrl}
          transport={transport}
          onTransportChange={setTransport}
          authType={authType}
          onAuthTypeChange={setAuthType}
          token={token}
          onTokenChange={setToken}
          editing={editing}
        />

        {/* Create has no `enabled` field — a new registration starts enabled — so
            the switch only appears once there is a server to turn off. */}
        {editing && (
          <Field>
            <FieldLabel>{t("agents.tools.enabled")}</FieldLabel>
            <Switch
              checked={enabled}
              aria-label={t("agents.tools.enabled")}
              onCheckedChange={(checked) => setEnabled(!!checked)}
            />
            <FieldDescription>{t("mcp.enabled.description")}</FieldDescription>
          </Field>
        )}
      </div>

      <div className="flex shrink-0 items-center justify-end gap-2 border-t p-4">
        <Button variant="ghost" disabled={save.isPending} onClick={onDone}>
          {t("common.cancel")}
        </Button>
        <Button
          loading={save.isPending && editing}
          disabled={!canSave}
          onClick={() => (editing ? save.mutate() : setConfirming(true))}
        >
          {editing ? t("common.save") : t("common.next")}
        </Button>
      </div>

      {confirming && (
        <ScopeConfirmStep
          title={t("mcp.scopeWhere")}
          subtitle={t("mcp.scopeWhereDesc", { name: name.trim() })}
          options={scopes.map((s) => ({
            value: s,
            label: t(SCOPE_LABEL_KEY[s]),
            description: t(SCOPE_DESC_KEY[s]),
          }))}
          value={scope}
          onValueChange={setScope}
          confirmLabel={t("mcp.add")}
          busy={save.isPending}
          onConfirm={() => save.mutate()}
          onCancel={() => setConfirming(false)}
        />
      )}
    </div>
  );
}
