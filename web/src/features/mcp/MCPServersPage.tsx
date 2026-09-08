import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  createMcpServer,
  deleteMcpServer,
  disconnectMcpServerOAuth,
  getMcpServerFile,
  probeMcpServer,
  startMcpServerOAuth,
  updateMcpServer,
  updateMcpServerCredentials,
  updateMcpServerFile,
} from "@/lib/api-client/sdk.gen";
import type { McpDeclaration, McpServer, ResourceFileContent } from "@/lib/api-client/types.gen";
import { mcpServersQueryOptions } from "@/lib/queries/mcp";
import { allAgentsAdminQueryOptions, agentsQueryOptions } from "@/lib/queries/agents";
import {
  isAgentManagedScope,
  scopesForBand,
  type ManagedScope,
  type ScopeBand,
} from "@/lib/scope-band";
import { ErrorState } from "@/components/RouteFallback";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Field, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectItem,
  SelectPopup,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Spinner } from "@/components/ui/spinner";
import { DetailPanel, DetailPanelHeader } from "@/features/settings/SettingsDetailPanel";
import { McpInstallSheet } from "./McpInstallSheet";
import { ensureMcpBearerCredentialRef, transitionMcpAuthType } from "./mcp-credential";
import { useToast } from "@/hooks/use-toast";
import { useI18n } from "@/lib/i18n";
import { apiErrorMessage } from "@/lib/api-error";
import { RefreshCw, Wifi, WifiOff, X } from "lucide-react";

function toBase64(value: string) {
  return btoa(unescape(encodeURIComponent(value)));
}
function fromBase64(value: string) {
  try {
    return decodeURIComponent(escape(atob(value)));
  } catch {
    return atob(value);
  }
}
function scopeLabel(scope: ManagedScope, t: ReturnType<typeof useI18n>["t"]) {
  return t(`plugins.scope.${scope}` as never);
}

function ScopePicker({
  band,
  scope,
  agentId,
  agents,
  onScope,
  onAgent,
}: {
  band: ScopeBand;
  scope: ManagedScope;
  agentId: string;
  agents: Array<{ id?: string; name?: string }>;
  onScope: (value: ManagedScope) => void;
  onAgent: (value: string) => void;
}) {
  const { t } = useI18n();
  return (
    <div className="flex flex-wrap items-end gap-2">
      <Field className="min-w-40">
        <FieldLabel>{t("plugins.scopeLabel")}</FieldLabel>
        <Select value={scope} onValueChange={(value) => value && onScope(value as ManagedScope)}>
          <SelectTrigger>
            <SelectValue>{(value) => scopeLabel((value as ManagedScope) || scope, t)}</SelectValue>
          </SelectTrigger>
          <SelectPopup>
            {scopesForBand(band).map((value) => (
              <SelectItem key={value} value={value}>
                {scopeLabel(value, t)}
              </SelectItem>
            ))}
          </SelectPopup>
        </Select>
      </Field>
      {isAgentManagedScope(scope) && (
        <Field className="min-w-52">
          <FieldLabel>{t("plugins.agent")}</FieldLabel>
          <Select
            value={agentId || "__none"}
            onValueChange={(value) => value && onAgent(value === "__none" ? "" : value)}
          >
            <SelectTrigger>
              <SelectValue placeholder={t("plugins.selectAgent")} />
            </SelectTrigger>
            <SelectPopup>
              <SelectItem value="__none">{t("plugins.selectAgent")}</SelectItem>
              {agents.map((agent) =>
                agent.id ? (
                  <SelectItem key={agent.id} value={agent.id}>
                    {agent.name || agent.id}
                  </SelectItem>
                ) : null,
              )}
            </SelectPopup>
          </Select>
        </Field>
      )}
    </div>
  );
}

function DeclarationFields({
  declaration,
  onChange,
  disabled,
}: {
  declaration: McpDeclaration;
  onChange: (value: McpDeclaration) => void;
  disabled?: boolean;
}) {
  const { t } = useI18n();
  const set = (patch: Partial<McpDeclaration>) => onChange({ ...declaration, ...patch });
  return (
    <div className="space-y-3">
      <Field>
        <FieldLabel>{t("mcp.url")}</FieldLabel>
        <Input
          value={declaration.url}
          onChange={(event) => set({ url: event.target.value })}
          disabled={disabled}
          nativeInput
        />
      </Field>
      <Field>
        <FieldLabel>{t("mcp.transport")}</FieldLabel>
        <Select
          value={declaration.transport}
          onValueChange={(value) =>
            value && set({ transport: value as McpDeclaration["transport"] })
          }
          disabled={disabled}
        >
          <SelectTrigger>
            <SelectValue />
          </SelectTrigger>
          <SelectPopup>
            <SelectItem value="streamable_http">Streamable HTTP</SelectItem>
            <SelectItem value="sse">SSE</SelectItem>
          </SelectPopup>
        </Select>
      </Field>
      <Field>
        <FieldLabel>{t("mcp.auth")}</FieldLabel>
        <Select
          value={declaration.auth_type}
          onValueChange={(value) =>
            value &&
            onChange(transitionMcpAuthType(declaration, value as McpDeclaration["auth_type"]))
          }
          disabled={disabled}
        >
          <SelectTrigger>
            <SelectValue />
          </SelectTrigger>
          <SelectPopup>
            <SelectItem value="none">{t("mcp.auth.none")}</SelectItem>
            <SelectItem value="bearer">{t("mcp.auth.bearer")}</SelectItem>
            <SelectItem value="oauth">{t("mcp.auth.oauth")}</SelectItem>
          </SelectPopup>
        </Select>
      </Field>
      <Field>
        <FieldLabel>{t("mcp.credentialMode")}</FieldLabel>
        <Select
          value={declaration.credential_mode}
          onValueChange={(value) =>
            value && set({ credential_mode: value as McpDeclaration["credential_mode"] })
          }
          disabled={disabled}
        >
          <SelectTrigger>
            <SelectValue />
          </SelectTrigger>
          <SelectPopup>
            <SelectItem value="shared">{t("mcp.credentialMode.shared")}</SelectItem>
            <SelectItem value="per_user">{t("mcp.credentialMode.perUser")}</SelectItem>
          </SelectPopup>
        </Select>
      </Field>
      <Field>
        <FieldLabel>{t("mcp.addDescription")}</FieldLabel>
        <Input
          value={declaration.description ?? ""}
          onChange={(event) => set({ description: event.target.value || undefined })}
          disabled={disabled}
          nativeInput
        />
      </Field>
      <Field>
        <FieldLabel>{t("mcp.scope")}</FieldLabel>
        <Input
          value={(declaration.scopes ?? []).join(", ")}
          onChange={(event) =>
            set({
              scopes: event.target.value
                .split(",")
                .map((item) => item.trim())
                .filter(Boolean),
            })
          }
          disabled={disabled}
          nativeInput
        />
      </Field>
    </div>
  );
}

function McpDetail({
  server,
  onRefresh,
  onClose,
}: {
  server: McpServer;
  onRefresh: () => void;
  onClose: () => void;
}) {
  const { t } = useI18n();
  const { showToast } = useToast();
  const queryClient = useQueryClient();
  const [declaration, setDeclaration] = useState<McpDeclaration | null>(server.declaration);
  const [raw, setRaw] = useState("");
  const [rawMode, setRawMode] = useState(false);
  const [token, setToken] = useState("");
  const [clientSecret, setClientSecret] = useState("");
  const invalidate = () => {
    void queryClient.invalidateQueries({ queryKey: ["mcp-servers"] });
    onRefresh();
  };
  useEffect(() => {
    setDeclaration(server.declaration);
    setRaw("");
    setRawMode(false);
    setToken("");
    setClientSecret("");
  }, [server.id, server.content_digest]);
  const saveDeclaration = useMutation({
    mutationFn: () => {
      if (!declaration) throw new Error(t("mcp.declarationRequired"));
      const nextDeclaration = transitionMcpAuthType(declaration, declaration.auth_type);
      return updateMcpServer({
        path: { id: server.id },
        body: {
          declaration: nextDeclaration,
          expected_digest: server.content_digest,
        },
        throwOnError: true,
      });
    },
    onSuccess: () => {
      showToast(t("mcp.updated"), "success");
      invalidate();
    },
    onError: (error) => showToast(apiErrorMessage(error, t("mcp.saveFailed")), "error"),
  });
  const saveRaw = useMutation({
    mutationFn: () =>
      updateMcpServerFile({
        path: { id: server.id },
        body: {
          content_base64: toBase64(raw),
          expected_digest: server.content_digest,
        },
        throwOnError: true,
      }),
    onSuccess: () => {
      showToast(t("mcp.updated"), "success");
      invalidate();
    },
    onError: (error) => showToast(apiErrorMessage(error, t("mcp.saveFailed")), "error"),
  });
  const saveCredentials = useMutation({
    mutationFn: () => {
      if (declaration?.auth_type === "bearer") {
        if (!token.trim()) throw new Error(t("mcp.credentialsRequired"));
        return updateMcpServerCredentials({
          path: { id: server.id },
          body: {
            expected_digest: server.content_digest,
            bearer_token: token.trim(),
          },
          throwOnError: true,
        });
      }
      if (declaration?.auth_type === "oauth") {
        if (!clientSecret) throw new Error(t("mcp.credentialsRequired"));
        return updateMcpServerCredentials({
          path: { id: server.id },
          body: {
            expected_digest: server.content_digest,
            client_secret: clientSecret,
          },
          throwOnError: true,
        });
      }
      throw new Error(t("mcp.credentialsRequired"));
    },
    onSuccess: () => {
      showToast(t("mcp.credentialsSaved"), "success");
      setToken("");
      setClientSecret("");
      invalidate();
    },
    onError: (error) => showToast(apiErrorMessage(error, t("mcp.saveFailed")), "error"),
  });
  const probe = useMutation({
    mutationFn: () => probeMcpServer({ path: { id: server.id }, throwOnError: true }),
    onSuccess: () => {
      showToast(t("mcp.server.probed"), "success");
      invalidate();
    },
    onError: (error) => showToast(apiErrorMessage(error, t("mcp.saveFailed")), "error"),
  });
  const connect = useMutation({
    mutationFn: () =>
      startMcpServerOAuth({
        path: { id: server.id },
        body: { expected_digest: server.content_digest },
        throwOnError: true,
      }),
    onSuccess: ({ data }) => {
      if (data?.authorization_url) window.location.href = data.authorization_url;
    },
    onError: (error) => showToast(apiErrorMessage(error, t("mcp.connectFailed")), "error"),
  });
  const disconnect = useMutation({
    mutationFn: () => disconnectMcpServerOAuth({ path: { id: server.id }, throwOnError: true }),
    onSuccess: invalidate,
    onError: (error) => showToast(apiErrorMessage(error, t("mcp.disconnectFailed")), "error"),
  });
  const remove = useMutation({
    mutationFn: () =>
      deleteMcpServer({
        path: { id: server.id },
        query: { expected_digest: server.content_digest },
        throwOnError: true,
      }),
    onSuccess: () => {
      showToast(t("mcp.deleted"), "success");
      onClose();
      invalidate();
    },
    onError: (error) => showToast(apiErrorMessage(error, t("mcp.deleteFailed")), "error"),
  });
  const readFile = async () => {
    try {
      const { data } = await getMcpServerFile({
        path: { id: server.id },
        throwOnError: true,
      });
      setRaw(fromBase64((data as ResourceFileContent).content_base64));
      setRawMode(true);
    } catch (error) {
      showToast(apiErrorMessage(error, t("mcp.saveFailed")), "error");
    }
  };
  const blocked = server.is_read_only || !server.is_standalone;
  return (
    <DetailPanel>
      <DetailPanelHeader
        title={server.name}
        action={
          <Button variant="ghost" size="icon-sm" aria-label={t("common.close")} onClick={onClose}>
            <X size={16} />
          </Button>
        }
      />
      <div className="space-y-4 overflow-y-auto p-4">
        <div className="flex flex-wrap gap-2">
          <Badge variant="outline">{scopeLabel(server.scope, t)}</Badge>
          <Badge
            variant={
              server.status === "ready"
                ? "success"
                : server.status === "error"
                  ? "destructive"
                  : "warning"
            }
          >
            {server.status}
          </Badge>
          {server.is_overridden && <Badge variant="warning">{t("plugins.overridden")}</Badge>}
          {!server.is_standalone && <Badge variant="secondary">{t("mcp.packageSource")}</Badge>}
        </div>
        {server.diagnostics.map((diagnostic, index) => (
          <div
            key={`${diagnostic.code}-${index}`}
            className="rounded-md border border-border p-2 text-xs"
          >
            <Badge variant={diagnostic.severity === "error" ? "destructive" : "warning"} size="sm">
              {diagnostic.severity}
            </Badge>{" "}
            {diagnostic.message}
          </div>
        ))}
        {declaration && !rawMode && (
          <DeclarationFields
            declaration={declaration}
            onChange={setDeclaration}
            disabled={blocked}
          />
        )}
        {rawMode && (
          <textarea
            value={raw}
            onChange={(event) => setRaw(event.target.value)}
            className="min-h-72 w-full rounded-md border border-border bg-background p-3 font-mono text-xs"
            aria-label={t("mcp.fileContent")}
            disabled={blocked}
          />
        )}
        {!blocked && (
          <div className="flex flex-wrap justify-end gap-2">
            <Button
              variant="outline"
              onClick={() => {
                setRawMode(!rawMode);
                if (!rawMode && !raw) void readFile();
              }}
            >
              {rawMode ? t("mcp.formMode") : t("mcp.jsonMode")}
            </Button>
            <Button
              onClick={() => (rawMode ? saveRaw.mutate() : saveDeclaration.mutate())}
              loading={saveRaw.isPending || saveDeclaration.isPending}
            >
              {t("common.save")}
            </Button>
          </div>
        )}
        {!server.is_standalone && (
          <p className="text-sm text-muted-foreground">{t("mcp.packageEditHint")}</p>
        )}
        <div className="space-y-2 rounded-lg border border-border p-3">
          <p className="text-xs font-semibold text-muted-foreground">{t("mcp.credentials")}</p>
          <Input
            type="password"
            value={token}
            onChange={(event) => setToken(event.target.value)}
            placeholder={t("mcp.token")}
            nativeInput
            disabled={server.is_read_only}
          />
          <Input
            type="password"
            value={clientSecret}
            onChange={(event) => setClientSecret(event.target.value)}
            placeholder={t("mcp.oauth.clientSecret")}
            nativeInput
            disabled={server.is_read_only}
          />
          <Button
            variant="outline"
            onClick={() => saveCredentials.mutate()}
            disabled={server.is_read_only || saveCredentials.isPending}
            loading={saveCredentials.isPending}
          >
            {t("mcp.saveCredentials")}
          </Button>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button variant="outline" onClick={() => probe.mutate()} loading={probe.isPending}>
            <RefreshCw size={16} />
            {t("mcp.server.probe")}
          </Button>
          {server.needs_auth && (
            <Button variant="outline" onClick={() => connect.mutate()} loading={connect.isPending}>
              <Wifi size={16} />
              {t("mcp.connect")}
            </Button>
          )}
          {!server.needs_auth && server.declaration?.auth_type === "oauth" && (
            <Button
              variant="outline"
              onClick={() => disconnect.mutate()}
              loading={disconnect.isPending}
            >
              <WifiOff size={16} />
              {t("mcp.disconnect")}
            </Button>
          )}
          {server.is_standalone && (
            <Button
              variant="destructive"
              onClick={() => remove.mutate()}
              loading={remove.isPending}
            >
              {t("common.delete")}
            </Button>
          )}
        </div>
      </div>
    </DetailPanel>
  );
}

function CreateMcp({
  band,
  agents,
  onDone,
}: {
  band: ScopeBand;
  agents: Array<{ id?: string; name?: string }>;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const { showToast } = useToast();
  const [name, setName] = useState("");
  const [scope, setScope] = useState<ManagedScope>(scopesForBand(band)[0]);
  const [agentId, setAgentId] = useState("");
  const [url, setURL] = useState("");
  const [transport, setTransport] = useState<McpDeclaration["transport"]>("streamable_http");
  const [auth, setAuth] = useState<McpDeclaration["auth_type"]>("none");
  const create = useMutation({
    mutationFn: () => {
      if (isAgentManagedScope(scope) && !agentId) throw new Error(t("plugins.selectAgent"));
      return createMcpServer({
        body: {
          name: name.trim(),
          scope,
          ...(agentId ? { agent_id: agentId } : {}),
          declaration: {
            url: url.trim(),
            transport,
            auth_type: auth,
            credential_mode: scope === "system" || scope === "system_agent" ? "shared" : "per_user",
            ...(auth === "bearer" ? { credential_ref: ensureMcpBearerCredentialRef() } : {}),
          },
        },
        throwOnError: true,
      });
    },
    onSuccess: () => {
      showToast(t("mcp.created"), "success");
      onDone();
      setName("");
      setURL("");
    },
    onError: (error) => showToast(apiErrorMessage(error, t("mcp.saveFailed")), "error"),
  });
  return (
    <div className="space-y-3 rounded-lg border border-border p-4">
      <p className="text-sm font-semibold">{t("mcp.create")}</p>
      <Input
        value={name}
        onChange={(event) => setName(event.target.value)}
        placeholder={t("mcp.name")}
        nativeInput
      />
      <ScopePicker
        band={band}
        scope={scope}
        agentId={agentId}
        agents={agents}
        onScope={setScope}
        onAgent={setAgentId}
      />
      <Input
        value={url}
        onChange={(event) => setURL(event.target.value)}
        placeholder="https://mcp.example.com/mcp"
        nativeInput
      />
      <div className="flex flex-wrap gap-2">
        <Select
          value={transport}
          onValueChange={(value) => value && setTransport(value as McpDeclaration["transport"])}
        >
          <SelectTrigger>
            <SelectValue />
          </SelectTrigger>
          <SelectPopup>
            <SelectItem value="streamable_http">Streamable HTTP</SelectItem>
            <SelectItem value="sse">SSE</SelectItem>
          </SelectPopup>
        </Select>
        <Select
          value={auth}
          onValueChange={(value) => value && setAuth(value as McpDeclaration["auth_type"])}
        >
          <SelectTrigger>
            <SelectValue />
          </SelectTrigger>
          <SelectPopup>
            <SelectItem value="none">{t("mcp.auth.none")}</SelectItem>
            <SelectItem value="bearer">{t("mcp.auth.bearer")}</SelectItem>
            <SelectItem value="oauth">{t("mcp.auth.oauth")}</SelectItem>
          </SelectPopup>
        </Select>
      </div>
      <Button
        onClick={() => create.mutate()}
        disabled={!name.trim() || !url.trim() || create.isPending}
        loading={create.isPending}
      >
        {t("common.create")}
      </Button>
    </div>
  );
}

export function MCPServersPage({ scopeBand }: { scopeBand: ScopeBand }) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { showToast } = useToast();
  const agentsQuery = useQuery(
    scopeBand === "system" ? allAgentsAdminQueryOptions(true) : agentsQueryOptions,
  );
  const agents = agentsQuery.data ?? [];
  const [agentId, setAgentId] = useState("");
  const [scope, setScope] = useState<ManagedScope>(scopesForBand(scopeBand)[0]);
  const [selected, setSelected] = useState<McpServer | null>(null);
  const [marketOpen, setMarketOpen] = useState(false);
  const query = useQuery(mcpServersQueryOptions(scopeBand, agentId || undefined));
  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: ["mcp-servers"] });
  };
  if (query.isPending)
    return (
      <div className="flex flex-1 items-center justify-center">
        <Spinner />
      </div>
    );
  if (query.isError)
    return (
      <ErrorState
        title={t("mcp.loadFailed")}
        description={apiErrorMessage(query.error, t("mcp.saveFailed"))}
        onRetry={() => void query.refetch()}
      />
    );
  const servers = query.data.filter((item) => item.scope === scope);
  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-y-auto p-4 sm:p-8">
      <div className="mx-auto flex w-full max-w-6xl flex-col gap-4">
        <div className="flex flex-wrap items-end justify-between gap-3">
          <div>
            <h1 className="text-lg font-semibold">{t("mcp.title")}</h1>
            <p className="text-sm text-muted-foreground">{t("mcp.rawResourceHelp")}</p>
          </div>
          <div className="flex gap-2">
            <Button variant="outline" onClick={() => setMarketOpen(true)}>
              {t("mcp.market.title")}
            </Button>
            <Button variant="outline" onClick={refresh}>
              <RefreshCw size={16} />
              {t("plugins.refresh")}
            </Button>
          </div>
        </div>
        <ScopePicker
          band={scopeBand}
          scope={scope}
          agentId={agentId}
          agents={agents}
          onScope={setScope}
          onAgent={setAgentId}
        />
        <CreateMcp band={scopeBand} agents={agents} onDone={refresh} />
        <div className="grid gap-3 md:grid-cols-2">
          {servers.length === 0 ? (
            <ErrorState title={t("mcp.noServers")} description={t("mcp.noServersDesc")} />
          ) : (
            servers.map((server) => (
              <Card key={server.id} className="flex flex-col gap-3 p-4">
                <button type="button" className="text-left" onClick={() => setSelected(server)}>
                  <div className="flex items-start justify-between gap-3">
                    <div>
                      <p className="font-medium">{server.name}</p>
                      <p className="font-mono text-xs text-muted-foreground">{server.server_key}</p>
                    </div>
                    <Badge
                      variant={
                        server.status === "ready"
                          ? "success"
                          : server.status === "error"
                            ? "destructive"
                            : "warning"
                      }
                    >
                      {server.status}
                    </Badge>
                  </div>
                  <div className="mt-2 flex flex-wrap gap-1.5">
                    <Badge variant="outline" size="sm">
                      {scopeLabel(server.scope, t)}
                    </Badge>
                    {server.is_overridden && (
                      <Badge variant="warning" size="sm">
                        {t("plugins.overridden")}
                      </Badge>
                    )}
                    {!server.is_standalone && (
                      <Badge variant="secondary" size="sm">
                        {t("mcp.packageSource")}
                      </Badge>
                    )}
                    <Badge variant="secondary" size="sm">
                      {server.tools.length} {t("mcp.server.tools")}
                    </Badge>
                  </div>
                </button>
              </Card>
            ))
          )}
        </div>
        {selected && (
          <McpDetail server={selected} onRefresh={refresh} onClose={() => setSelected(null)} />
        )}
        <McpInstallSheet
          open={marketOpen}
          onOpenChange={setMarketOpen}
          notify={(message, kind) => showToast(message, kind === "error" ? "error" : "success")}
          defaultScope={scope}
          agentId={agentId || undefined}
          isAdmin={scopeBand === "system"}
        />
      </div>
    </div>
  );
}

export function PersonalMCPPage() {
  return <MCPServersPage scopeBand="personal" />;
}
export function GlobalMCPPage() {
  return <MCPServersPage scopeBand="system" />;
}
