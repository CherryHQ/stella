import { ApiClient, expectStatus } from "./api.ts";

export interface ToolCallEvent {
  toolCallId: string;
  toolName: string;
  input?: unknown;
}

export interface TurnResult {
  status: number;
  events: Record<string, unknown>[];
  text: string;
  toolCalls: ToolCallEvent[];
  errors: string[];
}

// Creates (or finds) an agent bound to the given model ref. A fresh testbed
// has no model configured, so the built-in agent has no runtime; an agent with
// an explicit model is what makes a chat turn possible.
export async function ensureAgent(api: ApiClient, modelRef: string, name = "e2e-agent"): Promise<string> {
  const list = expectStatus(await api.get<{ agents: { id: string; name: string }[] }>("/api/agents"), 200, "list agents");
  const existing = list.agents.find((a) => a.name === name);
  if (existing) return existing.id;
  const created = expectStatus(
    await api.post<{ id: string }>("/api/agents", { name, model: modelRef, enabled: true }),
    201,
    "create agent",
  );
  return created.id;
}

export async function createChatSession(api: ApiClient, agentId: string): Promise<string> {
  const body = expectStatus(
    await api.post<{ id: string }>(`/api/agents/${agentId}/sessions`, { agent_id: agentId, kind: "chat" }),
    201,
    "create session",
  );
  return body.id;
}

// Sends one user message and folds the UI-message stream into text, tool
// calls, and errors. Tool calls come from tool-input-available events, which
// carry the final arguments.
export async function sendTurn(api: ApiClient, agentId: string, sessionId: string, text: string): Promise<TurnResult> {
  const { status, events } = await api.stream(`/api/agents/${agentId}/sessions/${sessionId}/messages`, {
    parts: [{ type: "text", text }],
  });
  const result: TurnResult = { status, events, text: "", toolCalls: [], errors: [] };
  for (const evt of events) {
    switch (evt.type) {
      case "text-delta":
        result.text += String(evt.delta ?? "");
        break;
      case "tool-input-available":
        result.toolCalls.push({
          toolCallId: String(evt.toolCallId),
          toolName: String(evt.toolName),
          input: evt.input,
        });
        break;
      case "error":
        result.errors.push(String(evt.errorText ?? JSON.stringify(evt)));
        break;
      case "http-error":
        result.errors.push(`HTTP ${String(evt.status)}: ${String(evt.body)}`);
        break;
    }
  }
  return result;
}

export interface SessionMessage {
  id: string;
  role: string;
  tool_name?: string;
  content?: string;
  blocks?: { type: string; name?: string; arguments?: unknown; text?: string }[];
  // Present on a Code Mode outer tool result: the child tools the sandboxed
  // code invoked. Names only; arguments and results are deliberately omitted.
  child_calls?: { tool_name?: string; name?: string; tool?: string }[];
}

// Every tool name the transcript records, whether the model called the tool
// directly or through Code Mode's `code` tool (whose child calls are audited
// on the outer tool result).
export function invokedToolNames(messages: SessionMessage[]): string[] {
  const names: string[] = [];
  for (const m of messages) {
    for (const b of m.blocks ?? []) if (b.type === "tool_call" && b.name) names.push(b.name);
    for (const c of m.child_calls ?? []) {
      const name = c.tool_name ?? c.name ?? c.tool;
      if (name) names.push(name);
    }
  }
  return names;
}

export async function sessionMessages(api: ApiClient, agentId: string, sessionId: string): Promise<SessionMessage[]> {
  const body = expectStatus(
    await api.get<{ messages: SessionMessage[] }>(`/api/agents/${agentId}/sessions/${sessionId}/messages`),
    200,
    "get messages",
  );
  return body.messages;
}
