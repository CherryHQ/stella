import type { McpServer, McpStatus } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Switch } from "@/components/ui/switch";
import {
  formatTimestamp,
  mcpStatusFor,
  mcpStatusLabel,
  mcpStatusTone,
  newMcpServer,
  nextRowID,
  usesRemoteTransport,
  validateMcpServers,
} from "./pluginUtils";

interface Props {
  mcpServers: McpServer[];
  mcpStatuses: McpStatus[];
  mcpPluginEnabled: boolean;
  mcpSaving: boolean;
  mcpLastSavedAt: string;
  mcpIsDirty: boolean;
  onServersChange: (servers: McpServer[]) => void;
  onToggleMcpPlugin: (enabled: boolean) => void;
  onSave: () => void;
}

export function McpTab({
  mcpServers,
  mcpStatuses,
  mcpPluginEnabled,
  mcpSaving,
  mcpLastSavedAt,
  mcpIsDirty,
  onServersChange,
  onToggleMcpPlugin,
  onSave,
}: Props) {
  const validation = validateMcpServers(mcpServers);
  const mcpHasErrors =
    validation.global.length > 0 || validation.byIndex.some((e) => e.length > 0);

  const enabledCount = mcpServers.filter((s) => s.enabled).length;
  const runningCount = mcpStatuses.filter((s) => s.state === "running").length;
  const suppressedCount = mcpStatuses.filter((s) => s.suppressed).length;
  const discoveredToolCount = mcpStatuses.reduce(
    (total, s) => total + Number(s.discovered_tool_count || 0),
    0,
  );

  function addServer() {
    onServersChange([...mcpServers, newMcpServer()]);
  }

  function removeServer(index: number) {
    const next = [...mcpServers];
    next.splice(index, 1);
    onServersChange(next);
  }

  function duplicateServer(index: number) {
    const source = mcpServers[index];
    if (!source) return;
    const copy: McpServer = {
      id: nextRowID(),
      expanded: true,
      name: source.name ? source.name + "-copy" : "",
      enabled: source.enabled,
      transport: source.transport,
      command: source.command,
      url: source.url,
      timeout_seconds: source.timeout_seconds,
      args: source.args.map((a) => ({ id: nextRowID(), value: a.value })),
      env: source.env.map((e) => ({ id: nextRowID(), key: e.key, value: e.value })),
      headers: source.headers.map((h) => ({ id: nextRowID(), key: h.key, value: h.value })),
    };
    const next = [...mcpServers];
    next.splice(index + 1, 0, copy);
    onServersChange(next);
  }

  function toggleServerExpanded(index: number) {
    const next = [...mcpServers];
    next[index] = { ...next[index], expanded: !next[index].expanded };
    onServersChange(next);
  }

  function updateServer(index: number, partial: Partial<McpServer>) {
    const next = [...mcpServers];
    next[index] = { ...next[index], ...partial };
    onServersChange(next);
  }

  function addArg(serverIndex: number) {
    const server = mcpServers[serverIndex];
    updateServer(serverIndex, {
      args: [...server.args, { id: nextRowID(), value: "" }],
    });
  }

  function removeArg(serverIndex: number, argIndex: number) {
    const server = mcpServers[serverIndex];
    let next = [...server.args];
    next.splice(argIndex, 1);
    if (next.length === 0) next = [{ id: nextRowID(), value: "" }];
    updateServer(serverIndex, { args: next });
  }

  function updateArg(serverIndex: number, argIndex: number, value: string) {
    const server = mcpServers[serverIndex];
    const next = [...server.args];
    next[argIndex] = { ...next[argIndex], value };
    updateServer(serverIndex, { args: next });
  }

  function addKeyValue(serverIndex: number, field: "env" | "headers") {
    const server = mcpServers[serverIndex];
    updateServer(serverIndex, {
      [field]: [...server[field], { id: nextRowID(), key: "", value: "" }],
    });
  }

  function removeKeyValue(serverIndex: number, field: "env" | "headers", rowIndex: number) {
    const server = mcpServers[serverIndex];
    let next = [...server[field]];
    next.splice(rowIndex, 1);
    if (next.length === 0) next = [{ id: nextRowID(), key: "", value: "" }];
    updateServer(serverIndex, { [field]: next });
  }

  function updateKeyValue(
    serverIndex: number,
    field: "env" | "headers",
    rowIndex: number,
    key: "key" | "value",
    value: string,
  ) {
    const server = mcpServers[serverIndex];
    const next = [...server[field]];
    next[rowIndex] = { ...next[rowIndex], [key]: value };
    updateServer(serverIndex, { [field]: next });
  }

  function statusBadgeVariant(tone: string): "success" | "error" | "warning" | "info" | "outline" {
    if (tone === "success") return "success";
    if (tone === "error") return "error";
    if (tone === "warning") return "warning";
    if (tone === "info") return "info";
    return "outline";
  }

  return (
    <div>
      {/* MCP plugin toggle */}
      <div className="flex items-center justify-between gap-4 mb-6">
        <div className="space-y-1">
          <div className="flex items-center gap-3">
            <p className="text-sm font-medium">MCP plugin</p>
            <Badge variant={mcpPluginEnabled ? "success" : "outline"} size="sm">
              {mcpPluginEnabled ? "enabled" : "disabled"}
            </Badge>
          </div>
          <p className="text-xs text-muted-foreground">
            Toggle the <span className="font-mono">tool/mcp</span> plugin. Servers below are
            managed independently.
          </p>
        </div>
        <Switch
          checked={mcpPluginEnabled}
          onCheckedChange={onToggleMcpPlugin}
        />
      </div>

      {/* Stats */}
      <div className="grid grid-cols-2 lg:grid-cols-4 gap-3 mb-6">
        <div className="rounded-lg border border-border bg-card p-3">
          <p className="text-[11px] uppercase tracking-wide text-muted-foreground">Servers</p>
          <p className="text-lg font-semibold">
            {enabledCount} / {mcpServers.length}
          </p>
        </div>
        <div className="rounded-lg border border-border bg-card p-3">
          <p className="text-[11px] uppercase tracking-wide text-muted-foreground">Running</p>
          <p className="text-lg font-semibold">{runningCount}</p>
        </div>
        <div className="rounded-lg border border-border bg-card p-3">
          <p className="text-[11px] uppercase tracking-wide text-muted-foreground">Tools discovered</p>
          <p className="text-lg font-semibold">{discoveredToolCount}</p>
        </div>
        <div className="rounded-lg border border-border bg-card p-3">
          <p className="text-[11px] uppercase tracking-wide text-muted-foreground">Suppressed</p>
          <p className={`text-lg font-semibold${suppressedCount > 0 ? " text-destructive" : ""}`}>
            {suppressedCount}
          </p>
        </div>
      </div>

      {/* Warnings */}
      {!mcpPluginEnabled && (
        <div className="rounded-lg border border-warning/40 bg-warning/10 px-3 py-2 text-xs text-warning-foreground mb-4">
          The MCP plugin is disabled. Saved configs stay intact, but no server will connect until
          enabled.
        </div>
      )}
      {validation.global.length > 0 && (
        <div className="rounded-lg border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs text-destructive-foreground space-y-1 mb-4">
          {validation.global.map((error) => (
            <p key={error}>{error}</p>
          ))}
        </div>
      )}

      {/* Actions */}
      <div className="flex items-center justify-between gap-3 mb-4">
        <div className="flex items-center gap-2 text-xs text-muted-foreground">
          {mcpLastSavedAt && <span>Last saved {formatTimestamp(mcpLastSavedAt)}</span>}
          {!mcpSaving && mcpIsDirty && (
            <Badge variant="warning" size="sm">unsaved</Badge>
          )}
        </div>
        <div className="flex items-center gap-2">
          <Button onClick={addServer} variant="ghost" size="sm">
            Add server
          </Button>
          <Button
            onClick={onSave}
            disabled={mcpSaving || mcpHasErrors || !mcpIsDirty}
            loading={mcpSaving}
            variant="default"
            size="sm"
          >
            Save
          </Button>
        </div>
      </div>

      {/* Server list */}
      <div className="space-y-3">
        {mcpServers.map((server, index) => {
          const serverErrors = validation.byIndex[index] || [];
          const statusInfo = mcpStatusFor(server.name, mcpStatuses);
          const statusLabel = mcpStatusLabel(
            server.name,
            server.enabled,
            mcpPluginEnabled,
            mcpStatuses,
          );
          const statusTone = mcpStatusTone(server.name, mcpStatuses);

          return (
            <div key={server.id} className="rounded-xl border border-border bg-card overflow-hidden">
              {/* Server header */}
              <div
                className="flex items-center justify-between gap-3 px-4 py-3 cursor-pointer"
                onClick={() => toggleServerExpanded(index)}
              >
                <div className="flex items-center gap-2 min-w-0">
                  <span className="text-xs text-muted-foreground">{server.expanded ? "▾" : "▸"}</span>
                  <span className="font-medium text-sm truncate">
                    {server.name || `Server ${index + 1}`}
                  </span>
                  <Badge variant="outline" size="sm">{server.transport}</Badge>
                  <Badge variant={statusBadgeVariant(statusTone)} size="sm">{statusLabel}</Badge>
                  {statusInfo?.discovered_tool_count ? (
                    <Badge variant="info" size="sm">
                      {statusInfo.discovered_tool_count} tools
                    </Badge>
                  ) : null}
                </div>
                <div
                  className="flex items-center gap-2"
                  onClick={(e) => e.stopPropagation()}
                >
                  <Button
                    onClick={() => duplicateServer(index)}
                    variant="ghost"
                    size="xs"
                  >
                    Duplicate
                  </Button>
                  <Button
                    onClick={() => removeServer(index)}
                    variant="ghost"
                    size="xs"
                    className="text-destructive"
                  >
                    Remove
                  </Button>
                  <Switch
                    checked={server.enabled}
                    onCheckedChange={(checked) => updateServer(index, { enabled: checked })}
                  />
                </div>
              </div>

              {/* Server body */}
              {server.expanded && (
                <div className="px-4 pb-4 border-t border-border space-y-4">
                  {serverErrors.length > 0 && (
                    <div className="rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive-foreground space-y-1 mt-3">
                      {serverErrors.map((error) => (
                        <p key={error}>{error}</p>
                      ))}
                    </div>
                  )}
                  {statusInfo?.last_error && (
                    <div className="rounded-lg border border-warning/30 bg-warning/10 px-3 py-2 text-xs text-warning-foreground mt-3">
                      <p className="font-medium">Last runtime error</p>
                      <p className="font-mono break-all">{statusInfo.last_error}</p>
                    </div>
                  )}

                  {/* Config fields */}
                  <div className="grid grid-cols-1 lg:grid-cols-3 gap-4 pt-2">
                    <div className="space-y-1">
                      <label className="text-xs font-medium text-muted-foreground">Server name</label>
                      <Input
                        nativeInput
                        value={server.name}
                        onChange={(e) => updateServer(index, { name: (e.target as HTMLInputElement).value })}
                        type="text"
                        placeholder="github"
                        size="sm"
                      />
                    </div>
                    <div className="space-y-1">
                      <label className="text-xs font-medium text-muted-foreground">Transport</label>
                      <select
                        value={server.transport}
                        onChange={(e) => updateServer(index, { transport: e.target.value })}
                        className="select select-bordered select-sm w-full"
                      >
                        <option value="stdio">stdio</option>
                        <option value="sse">sse</option>
                        <option value="streamable_http">streamable_http</option>
                        <option value="http">http</option>
                      </select>
                    </div>
                    <div className="space-y-1">
                      <label className="text-xs font-medium text-muted-foreground">
                        Timeout (seconds)
                      </label>
                      <Input
                        nativeInput
                        value={server.timeout_seconds}
                        onChange={(e) =>
                          updateServer(index, { timeout_seconds: Number((e.target as HTMLInputElement).value) })
                        }
                        type="number"
                        min="0"
                        placeholder="30"
                        size="sm"
                      />
                    </div>
                  </div>

                  {/* Command / URL */}
                  <div className="space-y-1">
                    <label className="text-xs font-medium text-muted-foreground">
                      {server.transport === "stdio" ? "Command" : "Endpoint URL"}
                    </label>
                    {server.transport === "stdio" ? (
                      <Input
                        nativeInput
                        value={server.command}
                        onChange={(e) => updateServer(index, { command: (e.target as HTMLInputElement).value })}
                        type="text"
                        placeholder="npx"
                        size="sm"
                      />
                    ) : (
                      <Input
                        nativeInput
                        value={server.url}
                        onChange={(e) => updateServer(index, { url: (e.target as HTMLInputElement).value })}
                        type="text"
                        placeholder="https://example.com/mcp"
                        size="sm"
                      />
                    )}
                  </div>

                  {/* Arguments (stdio only) */}
                  {server.transport === "stdio" && (
                    <div className="space-y-2">
                      <div className="flex items-center justify-between gap-2">
                        <label className="text-xs font-medium text-muted-foreground">Arguments</label>
                        <Button onClick={() => addArg(index)} variant="ghost" size="xs">
                          Add
                        </Button>
                      </div>
                      {server.args.map((arg, argIndex) => (
                        <div key={arg.id} className="flex items-center gap-2">
                          <Input
                            nativeInput
                            value={arg.value}
                            onChange={(e) => updateArg(index, argIndex, (e.target as HTMLInputElement).value)}
                            type="text"
                            placeholder={`arg ${argIndex + 1}`}
                            className="font-mono"
                            size="sm"
                          />
                          <Button
                            onClick={() => removeArg(index, argIndex)}
                            variant="ghost"
                            size="xs"
                            className="text-destructive"
                          >
                            &times;
                          </Button>
                        </div>
                      ))}
                    </div>
                  )}

                  {/* Env vars (stdio only) */}
                  {server.transport === "stdio" && (
                    <div className="space-y-2">
                      <div className="flex items-center justify-between gap-2">
                        <label className="text-xs font-medium text-muted-foreground">
                          Environment variables
                        </label>
                        <Button
                          onClick={() => addKeyValue(index, "env")}
                          variant="ghost"
                          size="xs"
                        >
                          Add
                        </Button>
                      </div>
                      {server.env.map((row, rowIndex) => (
                        <div key={row.id} className="grid grid-cols-[1fr_1fr_auto] gap-2">
                          <Input
                            nativeInput
                            value={row.key}
                            onChange={(e) =>
                              updateKeyValue(index, "env", rowIndex, "key", (e.target as HTMLInputElement).value)
                            }
                            type="text"
                            placeholder="KEY"
                            className="font-mono"
                            size="sm"
                          />
                          <Input
                            nativeInput
                            value={row.value}
                            onChange={(e) =>
                              updateKeyValue(index, "env", rowIndex, "value", (e.target as HTMLInputElement).value)
                            }
                            type="text"
                            placeholder="value"
                            className="font-mono"
                            size="sm"
                          />
                          <Button
                            onClick={() => removeKeyValue(index, "env", rowIndex)}
                            variant="ghost"
                            size="xs"
                            className="text-destructive"
                          >
                            &times;
                          </Button>
                        </div>
                      ))}
                    </div>
                  )}

                  {/* Headers (remote only) */}
                  {usesRemoteTransport(server) && (
                    <div className="space-y-2">
                      <div className="flex items-center justify-between gap-2">
                        <label className="text-xs font-medium text-muted-foreground">HTTP headers</label>
                        <Button
                          onClick={() => addKeyValue(index, "headers")}
                          variant="ghost"
                          size="xs"
                        >
                          Add
                        </Button>
                      </div>
                      {server.headers.map((row, rowIndex) => (
                        <div key={row.id} className="grid grid-cols-[1fr_1fr_auto] gap-2">
                          <Input
                            nativeInput
                            value={row.key}
                            onChange={(e) =>
                              updateKeyValue(index, "headers", rowIndex, "key", (e.target as HTMLInputElement).value)
                            }
                            type="text"
                            placeholder="Authorization"
                            className="font-mono"
                            size="sm"
                          />
                          <Input
                            nativeInput
                            value={row.value}
                            onChange={(e) =>
                              updateKeyValue(index, "headers", rowIndex, "value", (e.target as HTMLInputElement).value)
                            }
                            type="text"
                            placeholder="Bearer ..."
                            className="font-mono"
                            size="sm"
                          />
                          <Button
                            onClick={() => removeKeyValue(index, "headers", rowIndex)}
                            variant="ghost"
                            size="xs"
                            className="text-destructive"
                          >
                            &times;
                          </Button>
                        </div>
                      ))}
                    </div>
                  )}

                  {/* Runtime status */}
                  <div className="rounded-lg border border-border p-3 space-y-2 text-sm">
                    <p className="text-xs font-medium text-muted-foreground">Runtime status</p>
                    <div className="grid grid-cols-2 lg:grid-cols-4 gap-x-6 gap-y-1 text-xs">
                      <div className="flex justify-between">
                        <span className="text-muted-foreground">State</span>
                        <span>{statusLabel}</span>
                      </div>
                      <div className="flex justify-between">
                        <span className="text-muted-foreground">Tools</span>
                        <span>{statusInfo?.discovered_tool_count || 0}</span>
                      </div>
                      <div className="flex justify-between">
                        <span className="text-muted-foreground">Failures</span>
                        <span>{statusInfo?.failures || 0}</span>
                      </div>
                      <div className="flex justify-between">
                        <span className="text-muted-foreground">Connected</span>
                        <span>{formatTimestamp(statusInfo?.last_connected_at ?? "") || "—"}</span>
                      </div>
                    </div>
                  </div>
                </div>
              )}
            </div>
          );
        })}

        {mcpServers.length === 0 && (
          <div className="rounded-lg border border-dashed border-border bg-card px-4 py-8 text-center space-y-2">
            <p className="text-sm font-medium">No MCP servers configured</p>
            <p className="text-xs text-muted-foreground">
              Add a server to connect a local stdio process or a remote HTTP/SSE endpoint.
            </p>
            <div>
              <Button onClick={addServer} variant="default" size="sm">
                Add server
              </Button>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
