import type { McpServer, McpStatus } from "@/lib/types";
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

  return (
    <div>
      {/* MCP plugin toggle */}
      <div className="flex items-center justify-between gap-4 mb-6">
        <div className="space-y-1">
          <div className="flex items-center gap-3">
            <p className="text-sm font-medium">MCP plugin</p>
            <span
              className={`badge badge-sm ${mcpPluginEnabled ? "badge-success" : "badge-ghost"}`}
            >
              {mcpPluginEnabled ? "enabled" : "disabled"}
            </span>
          </div>
          <p className="text-xs text-secondary">
            Toggle the <span className="font-mono">tool/mcp</span> plugin. Servers below are
            managed independently.
          </p>
        </div>
        <input
          type="checkbox"
          checked={mcpPluginEnabled}
          onChange={(e) => onToggleMcpPlugin(e.target.checked)}
          className="toggle toggle-primary toggle-sm"
        />
      </div>

      {/* Stats */}
      <div className="grid grid-cols-2 lg:grid-cols-4 gap-3 mb-6">
        <div className="rounded-lg border border-base-300 bg-base-100 p-3">
          <p className="text-[11px] uppercase tracking-wide text-secondary">Servers</p>
          <p className="text-lg font-semibold">
            {enabledCount} / {mcpServers.length}
          </p>
        </div>
        <div className="rounded-lg border border-base-300 bg-base-100 p-3">
          <p className="text-[11px] uppercase tracking-wide text-secondary">Running</p>
          <p className="text-lg font-semibold">{runningCount}</p>
        </div>
        <div className="rounded-lg border border-base-300 bg-base-100 p-3">
          <p className="text-[11px] uppercase tracking-wide text-secondary">Tools discovered</p>
          <p className="text-lg font-semibold">{discoveredToolCount}</p>
        </div>
        <div className="rounded-lg border border-base-300 bg-base-100 p-3">
          <p className="text-[11px] uppercase tracking-wide text-secondary">Suppressed</p>
          <p className={`text-lg font-semibold${suppressedCount > 0 ? " text-error" : ""}`}>
            {suppressedCount}
          </p>
        </div>
      </div>

      {/* Warnings */}
      {!mcpPluginEnabled && (
        <div className="rounded-lg border border-warning/40 bg-warning/10 px-3 py-2 text-xs text-warning-content mb-4">
          The MCP plugin is disabled. Saved configs stay intact, but no server will connect until
          enabled.
        </div>
      )}
      {validation.global.length > 0 && (
        <div className="rounded-lg border border-error/40 bg-error/10 px-3 py-2 text-xs text-error-content space-y-1 mb-4">
          {validation.global.map((error) => (
            <p key={error}>{error}</p>
          ))}
        </div>
      )}

      {/* Actions */}
      <div className="flex items-center justify-between gap-3 mb-4">
        <div className="flex items-center gap-2 text-xs text-secondary">
          {mcpLastSavedAt && <span>Last saved {formatTimestamp(mcpLastSavedAt)}</span>}
          {!mcpSaving && mcpIsDirty && (
            <span className="badge badge-warning badge-xs">unsaved</span>
          )}
        </div>
        <div className="flex items-center gap-2">
          <button onClick={addServer} className="btn btn-ghost btn-sm">
            Add server
          </button>
          <button
            onClick={onSave}
            disabled={mcpSaving || mcpHasErrors || !mcpIsDirty}
            className="btn btn-primary btn-sm"
          >
            {mcpSaving && <span className="loading loading-spinner loading-xs"></span>}
            Save
          </button>
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
            <div key={server.id} className="rounded-xl border border-base-300 bg-base-100 overflow-hidden">
              {/* Server header */}
              <div
                className="flex items-center justify-between gap-3 px-4 py-3 cursor-pointer"
                onClick={() => toggleServerExpanded(index)}
              >
                <div className="flex items-center gap-2 min-w-0">
                  <span className="text-xs text-secondary">{server.expanded ? "▾" : "▸"}</span>
                  <span className="font-medium text-sm truncate">
                    {server.name || `Server ${index + 1}`}
                  </span>
                  <span className="badge badge-ghost badge-xs">{server.transport}</span>
                  <span className={`badge badge-xs ${statusTone}`}>{statusLabel}</span>
                  {statusInfo?.discovered_tool_count ? (
                    <span className="badge badge-info badge-xs">
                      {statusInfo.discovered_tool_count} tools
                    </span>
                  ) : null}
                </div>
                <div
                  className="flex items-center gap-2"
                  onClick={(e) => e.stopPropagation()}
                >
                  <button
                    onClick={() => duplicateServer(index)}
                    className="btn btn-ghost btn-xs"
                  >
                    Duplicate
                  </button>
                  <button
                    onClick={() => removeServer(index)}
                    className="btn btn-ghost btn-xs text-error"
                  >
                    Remove
                  </button>
                  <label className="label cursor-pointer gap-2 py-0">
                    <input
                      type="checkbox"
                      checked={server.enabled}
                      onChange={(e) => updateServer(index, { enabled: e.target.checked })}
                      className="toggle toggle-primary toggle-sm"
                    />
                  </label>
                </div>
              </div>

              {/* Server body */}
              {server.expanded && (
                <div className="px-4 pb-4 border-t border-base-300 space-y-4">
                  {serverErrors.length > 0 && (
                    <div className="rounded-lg border border-error/30 bg-error/10 px-3 py-2 text-xs text-error-content space-y-1 mt-3">
                      {serverErrors.map((error) => (
                        <p key={error}>{error}</p>
                      ))}
                    </div>
                  )}
                  {statusInfo?.last_error && (
                    <div className="rounded-lg border border-warning/30 bg-warning/10 px-3 py-2 text-xs text-warning-content mt-3">
                      <p className="font-medium">Last runtime error</p>
                      <p className="font-mono break-all">{statusInfo.last_error}</p>
                    </div>
                  )}

                  {/* Config fields */}
                  <div className="grid grid-cols-1 lg:grid-cols-3 gap-4 pt-2">
                    <div className="space-y-1">
                      <label className="text-xs font-medium text-secondary">Server name</label>
                      <input
                        value={server.name}
                        onChange={(e) => updateServer(index, { name: e.target.value })}
                        type="text"
                        placeholder="github"
                        className="input input-bordered input-sm w-full"
                      />
                    </div>
                    <div className="space-y-1">
                      <label className="text-xs font-medium text-secondary">Transport</label>
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
                      <label className="text-xs font-medium text-secondary">
                        Timeout (seconds)
                      </label>
                      <input
                        value={server.timeout_seconds}
                        onChange={(e) =>
                          updateServer(index, { timeout_seconds: Number(e.target.value) })
                        }
                        type="number"
                        min="0"
                        placeholder="30"
                        className="input input-bordered input-sm w-full"
                      />
                    </div>
                  </div>

                  {/* Command / URL */}
                  <div className="space-y-1">
                    <label className="text-xs font-medium text-secondary">
                      {server.transport === "stdio" ? "Command" : "Endpoint URL"}
                    </label>
                    {server.transport === "stdio" ? (
                      <input
                        value={server.command}
                        onChange={(e) => updateServer(index, { command: e.target.value })}
                        type="text"
                        placeholder="npx"
                        className="input input-bordered input-sm w-full"
                      />
                    ) : (
                      <input
                        value={server.url}
                        onChange={(e) => updateServer(index, { url: e.target.value })}
                        type="text"
                        placeholder="https://example.com/mcp"
                        className="input input-bordered input-sm w-full"
                      />
                    )}
                  </div>

                  {/* Arguments (stdio only) */}
                  {server.transport === "stdio" && (
                    <div className="space-y-2">
                      <div className="flex items-center justify-between gap-2">
                        <label className="text-xs font-medium text-secondary">Arguments</label>
                        <button onClick={() => addArg(index)} className="btn btn-ghost btn-xs">
                          Add
                        </button>
                      </div>
                      {server.args.map((arg, argIndex) => (
                        <div key={arg.id} className="flex items-center gap-2">
                          <input
                            value={arg.value}
                            onChange={(e) => updateArg(index, argIndex, e.target.value)}
                            type="text"
                            placeholder={`arg ${argIndex + 1}`}
                            className="input input-bordered input-sm w-full font-mono"
                          />
                          <button
                            onClick={() => removeArg(index, argIndex)}
                            className="btn btn-ghost btn-xs text-error"
                          >
                            &times;
                          </button>
                        </div>
                      ))}
                    </div>
                  )}

                  {/* Env vars (stdio only) */}
                  {server.transport === "stdio" && (
                    <div className="space-y-2">
                      <div className="flex items-center justify-between gap-2">
                        <label className="text-xs font-medium text-secondary">
                          Environment variables
                        </label>
                        <button
                          onClick={() => addKeyValue(index, "env")}
                          className="btn btn-ghost btn-xs"
                        >
                          Add
                        </button>
                      </div>
                      {server.env.map((row, rowIndex) => (
                        <div key={row.id} className="grid grid-cols-[1fr_1fr_auto] gap-2">
                          <input
                            value={row.key}
                            onChange={(e) =>
                              updateKeyValue(index, "env", rowIndex, "key", e.target.value)
                            }
                            type="text"
                            placeholder="KEY"
                            className="input input-bordered input-sm w-full font-mono"
                          />
                          <input
                            value={row.value}
                            onChange={(e) =>
                              updateKeyValue(index, "env", rowIndex, "value", e.target.value)
                            }
                            type="text"
                            placeholder="value"
                            className="input input-bordered input-sm w-full font-mono"
                          />
                          <button
                            onClick={() => removeKeyValue(index, "env", rowIndex)}
                            className="btn btn-ghost btn-xs text-error"
                          >
                            &times;
                          </button>
                        </div>
                      ))}
                    </div>
                  )}

                  {/* Headers (remote only) */}
                  {usesRemoteTransport(server) && (
                    <div className="space-y-2">
                      <div className="flex items-center justify-between gap-2">
                        <label className="text-xs font-medium text-secondary">HTTP headers</label>
                        <button
                          onClick={() => addKeyValue(index, "headers")}
                          className="btn btn-ghost btn-xs"
                        >
                          Add
                        </button>
                      </div>
                      {server.headers.map((row, rowIndex) => (
                        <div key={row.id} className="grid grid-cols-[1fr_1fr_auto] gap-2">
                          <input
                            value={row.key}
                            onChange={(e) =>
                              updateKeyValue(index, "headers", rowIndex, "key", e.target.value)
                            }
                            type="text"
                            placeholder="Authorization"
                            className="input input-bordered input-sm w-full font-mono"
                          />
                          <input
                            value={row.value}
                            onChange={(e) =>
                              updateKeyValue(index, "headers", rowIndex, "value", e.target.value)
                            }
                            type="text"
                            placeholder="Bearer ..."
                            className="input input-bordered input-sm w-full font-mono"
                          />
                          <button
                            onClick={() => removeKeyValue(index, "headers", rowIndex)}
                            className="btn btn-ghost btn-xs text-error"
                          >
                            &times;
                          </button>
                        </div>
                      ))}
                    </div>
                  )}

                  {/* Runtime status */}
                  <div className="rounded-lg border border-base-300 p-3 space-y-2 text-sm">
                    <p className="text-xs font-medium text-secondary">Runtime status</p>
                    <div className="grid grid-cols-2 lg:grid-cols-4 gap-x-6 gap-y-1 text-xs">
                      <div className="flex justify-between">
                        <span className="text-secondary">State</span>
                        <span>{statusLabel}</span>
                      </div>
                      <div className="flex justify-between">
                        <span className="text-secondary">Tools</span>
                        <span>{statusInfo?.discovered_tool_count || 0}</span>
                      </div>
                      <div className="flex justify-between">
                        <span className="text-secondary">Failures</span>
                        <span>{statusInfo?.failures || 0}</span>
                      </div>
                      <div className="flex justify-between">
                        <span className="text-secondary">Connected</span>
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
          <div className="rounded-lg border border-dashed border-base-300 bg-base-100 px-4 py-8 text-center space-y-2">
            <p className="text-sm font-medium">No MCP servers configured</p>
            <p className="text-xs text-secondary">
              Add a server to connect a local stdio process or a remote HTTP/SSE endpoint.
            </p>
            <div>
              <button onClick={addServer} className="btn btn-primary btn-sm">
                Add server
              </button>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
