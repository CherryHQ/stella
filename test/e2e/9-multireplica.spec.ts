// Cross-replica durable session coverage: the turn always executes on the
// primary testbed replica (siblings run with STELLA_RUN_WORKER=off), so every
// event a sibling serves crossed PostgreSQL — live progress in a sibling
// browser, Last-Event-ID resume, and cancellation issued against the sibling.
import { createChatSession, ensureAgent } from "./lib/agent.ts";
import { ApiClient, expectStatus } from "./lib/api.ts";
import type { Sql } from "./lib/db.ts";
import { type FixtureServer, startFixtureServer } from "./lib/fixture-server.ts";
import { expect, loginWithPassword, test } from "./lib/fixtures.ts";
import { type SiblingReplica, startSiblingReplica } from "./lib/replica.ts";

test.describe.configure({ mode: "serial" });

const providerID = "e2e-gated-anthropic";
const modelName = "claude-sonnet-4-6";

interface Gate {
  // Resolves once the provider call arrived and the first delta was flushed —
  // the turn is genuinely in flight and its first events are being committed.
  entered: Promise<void>;
  release(): void;
}

interface Script {
  first: string;
  rest: string;
  markEntered(): void;
  gate: Promise<void>;
  release(): void;
}

function sseFrame(event: string, data: unknown): string {
  return `event: ${event}\ndata: ${JSON.stringify(data)}\n\n`;
}

// The minimal Anthropic messages-stream the provider adapter consumes, split
// at the first text delta so a script can pin the turn mid-flight.
function anthropicFrames(first: string, rest: string): { head: string[]; tail: string[]; } {
  const head = [
    sseFrame("message_start", {
      type: "message_start",
      message: {
        id: "msg_e2e",
        type: "message",
        role: "assistant",
        model: modelName,
        content: [],
        stop_reason: null,
        stop_sequence: null,
        usage: { input_tokens: 1, output_tokens: 1 },
      },
    }),
    sseFrame("content_block_start", { type: "content_block_start", index: 0, content_block: { type: "text", text: "" } }),
    sseFrame("content_block_delta", { type: "content_block_delta", index: 0, delta: { type: "text_delta", text: first } }),
  ];
  const tail = [
    ...(rest
      ? [sseFrame("content_block_delta", { type: "content_block_delta", index: 0, delta: { type: "text_delta", text: rest } })]
      : []),
    sseFrame("content_block_stop", { type: "content_block_stop", index: 0 }),
    sseFrame("message_delta", {
      type: "message_delta",
      delta: { stop_reason: "end_turn", stop_sequence: null },
      usage: { output_tokens: 5 },
    }),
    sseFrame("message_stop", { type: "message_stop" }),
  ];
  return { head, tail };
}

const scripts: Script[] = [];

function enqueueGate(first: string, rest: string): Gate {
  let markEntered!: () => void;
  let release!: () => void;
  const entered = new Promise<void>((resolve) => {
    markEntered = resolve;
  });
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  scripts.push({ first, rest, markEntered, gate, release });
  return { entered, release };
}

let model: FixtureServer;
let sibling: SiblingReplica;
let siblingAPI: ApiClient;
let agentID = "";

interface Frame {
  id?: string;
  data: Record<string, unknown>;
}

interface EventStream {
  frames: Frame[];
  done: Promise<void>;
  lastID(): string | undefined;
  close(): void;
}

// Read-only subscription to a session's live turn — the same endpoint the web
// UI's resume path uses, with an optional Last-Event-ID cursor.
function openEvents(client: ApiClient, sessionId: string, cursor?: string): EventStream {
  const frames: Frame[] = [];
  const controller = new AbortController();
  let lastID: string | undefined;
  const done = (async () => {
    const res = await fetch(`${client.baseURL}/api/agents/${agentID}/sessions/${sessionId}/events`, {
      headers: {
        Authorization: `Bearer ${client.token}`,
        Accept: "text/event-stream",
        ...(cursor ? { "Last-Event-ID": cursor } : {}),
      },
      signal: controller.signal,
    });
    if (res.status === 204 || !res.body) return;
    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";
    for (;;) {
      const { value, done: eof } = await reader.read();
      if (eof) break;
      buffer += decoder.decode(value, { stream: true });
      let index: number;
      while ((index = buffer.indexOf("\n\n")) >= 0) {
        const raw = buffer.slice(0, index);
        buffer = buffer.slice(index + 2);
        let data = "";
        for (const line of raw.split("\n")) {
          if (line.startsWith("id:")) lastID = line.slice(3).trim();
          else if (line.startsWith("data:")) data += line.slice(5).trim();
        }
        if (data === "[DONE]") return;
        if (!data) continue; // bare id: frame — cursor marker only
        try {
          frames.push({ id: lastID, data: JSON.parse(data) as Record<string, unknown> });
        } catch {
          frames.push({ id: lastID, data: { raw: data } });
        }
      }
    }
  })().catch(() => {});
  return {
    frames,
    done,
    lastID: () => lastID,
    close: () => controller.abort(),
  };
}

// streamText polls the collected frames until the streamed text contains want.
async function waitForText(stream: EventStream, want: string, what: string): Promise<void> {
  await expect
    .poll(
      () => stream.frames.map((f) => String(f.data.delta ?? "")).join(""),
      { timeout: 30_000, message: what },
    )
    .toContain(want);
}

async function waitForRunState(db: Sql, sessionId: string, states: string[], what: string): Promise<string> {
  let state = "";
  await expect
    .poll(
      async () => {
        const rows =
          (await db.unsafe("select state from agent_run where session_id = $1 order by created_at desc limit 1", [sessionId])) as {
            state: string;
          }[];
        state = rows[0]?.state ?? "";
        return states.includes(state);
      },
      { timeout: 30_000, message: what },
    )
    .toBe(true);
  return state;
}

test.beforeAll(async ({ creds, admin }) => {
  model = await startFixtureServer((req, res) => {
    if (req.method !== "POST" || !(req.url ?? "").includes("/messages")) {
      res.writeHead(404).end();
      return;
    }
    req.resume();
    const script = scripts.shift() ?? {
      first: "e2e default reply",
      rest: "",
      markEntered: () => {},
      gate: Promise.resolve(),
      release: () => {},
    };
    res.writeHead(200, { "content-type": "text/event-stream", "cache-control": "no-cache" });
    const { head, tail } = anthropicFrames(script.first, script.rest);
    for (const f of head) res.write(f);
    script.markEntered();
    const finish = () => {
      if (res.writableEnded || res.destroyed) return;
      for (const f of tail) res.write(f);
      res.end();
    };
    void script.gate.then(finish);
    // A cancelled turn drops the provider call — release the gate so the
    // script queue stays aligned and never block on a dead socket.
    res.on("close", () => script.release());
  });
  const list = expectStatus(await admin.get<{ providers: { id: string; }[]; }>("/api/providers"), 200, "list providers");
  if (!list.providers.some((p) => p.id === providerID)) {
    expectStatus(
      await admin.post("/api/providers", {
        id: providerID,
        type: "anthropic",
        name: "E2E Gated Anthropic",
        enabled: true,
        api_key: "e2e-gated-key",
        base_url: model.state.url,
        model_policy: "allow_all",
        models: { [modelName]: { id: modelName, enabled: true, input: ["text"] } },
      }),
      201,
      "register gated provider",
    );
  }
  agentID = await ensureAgent(admin, `${providerID}/${modelName}`, "e2e-replica-agent");
  // Boot the sibling only after the agent exists: a replica loads its agent
  // services once at startup, so a later-created agent would 404 there.
  sibling = await startSiblingReplica(creds);
  siblingAPI = new ApiClient(sibling.baseURL, creds.admin.token);
});

test.afterAll(async () => {
  await model?.close();
  await sibling?.stop();
});

test("sibling serves the in-flight turn's committed events and a cursor resume", async ({ admin, db }) => {
  const sessionID = await createChatSession(admin, agentID);
  const gate = enqueueGate("ALPHA-segment-one ", "BETA-segment-two");
  const send = admin.stream(`/api/agents/${agentID}/sessions/${sessionID}/messages`, { parts: [{ type: "text", text: "hi" }] });
  await gate.entered;

  // The run executes on the primary; the sibling has no worker (RUN_WORKER=off),
  // so its events stream is served from the durable log — cross-process proof.
  const first = openEvents(siblingAPI, sessionID);
  try {
    await waitForText(first, "ALPHA-segment-one", "sibling observes committed delta mid-gate");
    const cursor = first.lastID();
    expect(cursor, "durable cursor is emitted as an SSE id").toMatch(/:/);
    first.close();
    await first.done;

    // Cursor resume: the replayed stream starts after the cursor — none of the
    // earlier deltas repeat.
    const resumed = openEvents(siblingAPI, sessionID, cursor);
    try {
      gate.release();
      await waitForText(resumed, "BETA-segment-two", "resumed stream sees the post-gate delta");
      await expect
        .poll(() => resumed.frames.length, { timeout: 30_000, message: "resumed stream terminates" })
        .toBeGreaterThan(0);
      await resumed.done;
      const resumedText = resumed.frames.map((f) => String(f.data.delta ?? "")).join("");
      expect(resumedText).not.toContain("ALPHA-segment-one");
    } finally {
      resumed.close();
    }
  } finally {
    first.close();
    gate.release();
  }
  await send;
  await waitForRunState(db, sessionID, ["completed"], "run completes after gate release");
});

test("a sibling browser watches the live turn and keeps it after reload", async ({ admin, creds, browser }) => {
  const sessionID = await createChatSession(admin, agentID);
  const gate = enqueueGate("LIVE-progress-part ", "LIVE-progress-final");
  const send = admin.stream(`/api/agents/${agentID}/sessions/${sessionID}/messages`, {
    parts: [{ type: "text", text: "show me progress" }],
  });
  try {
    await gate.entered;

    const ctx = await browser.newContext({ baseURL: sibling.baseURL });
    const page = await ctx.newPage();
    try {
      await loginWithPassword(page, creds.admin.email, creds.admin.password);
      await page.goto(`/agents/${agentID}/sessions/${sessionID}`);
      // Mid-gate: the committed first segment is visible on the sibling while
      // the primary's model call is still held — persisted live progress.
      await expect(page.getByText("LIVE-progress-part")).toBeVisible({ timeout: 30_000 });
      gate.release();
      await expect(page.getByText("LIVE-progress-final")).toBeVisible({ timeout: 30_000 });
      // Reload = full reconnect: the transcript survives on the sibling.
      await page.reload();
      await expect(page.getByText("LIVE-progress-final")).toBeVisible({ timeout: 30_000 });
    } finally {
      await ctx.close();
    }
  } finally {
    gate.release();
  }
  await send;
});

test("stop issued to the sibling cancels the primary's in-flight run", async ({ admin, db }) => {
  const sessionID = await createChatSession(admin, agentID);
  const gate = enqueueGate("CANCEL-part-one ", "CANCEL-part-two");
  const send = admin.stream(`/api/agents/${agentID}/sessions/${sessionID}/messages`, { parts: [{ type: "text", text: "cancel me" }] });
  await gate.entered;

  // The sibling holds no worker — the stop write lands in the shared log and
  // the primary's executing turn observes it.
  const stopped = await siblingAPI.post(`/api/agents/${agentID}/sessions/${sessionID}/stop`, {});
  expect([200, 204], `stop on sibling: ${JSON.stringify(stopped.body)}`).toContain(stopped.status);

  const state = await waitForRunState(db, sessionID, ["canceled", "failed"], "run is terminal after sibling stop");
  expect(state).toBe("canceled");
  // The sender's own stream settles too — the turn is done, not just flagged.
  const result = await send;
  expect(result.status).toBe(200);
  gate.release();
});
