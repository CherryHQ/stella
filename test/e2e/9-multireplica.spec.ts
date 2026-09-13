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
  // A tool turn ends the response with stop_reason "tool_use" and runs no
  // gate — the tool executes server-side and its continuation request picks
  // up the next script.
  tool?: { id: string; name: string; args: string; };
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
    ...(first
      ? [sseFrame("content_block_delta", { type: "content_block_delta", index: 0, delta: { type: "text_delta", text: first } })]
      : []),
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

// A text block followed by a tool_use block, stop_reason "tool_use" — the
// real Anthropic shape for "say something, then call a tool". The runtime
// executes the tool and its continuation request consumes the next script.
function anthropicToolFrames(text: string, tool: { id: string; name: string; args: string; }): string[] {
  return [
    sseFrame("message_start", {
      type: "message_start",
      message: {
        id: "msg_e2e_tool",
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
    sseFrame("content_block_delta", { type: "content_block_delta", index: 0, delta: { type: "text_delta", text } }),
    sseFrame("content_block_stop", { type: "content_block_stop", index: 0 }),
    sseFrame("content_block_start", {
      type: "content_block_start",
      index: 1,
      content_block: { type: "tool_use", id: tool.id, name: tool.name, input: {} },
    }),
    sseFrame("content_block_delta", {
      type: "content_block_delta",
      index: 1,
      delta: { type: "input_json_delta", partial_json: tool.args },
    }),
    sseFrame("content_block_stop", { type: "content_block_stop", index: 1 }),
    sseFrame("message_delta", {
      type: "message_delta",
      delta: { stop_reason: "tool_use", stop_sequence: null },
      usage: { output_tokens: 5 },
    }),
    sseFrame("message_stop", { type: "message_stop" }),
  ];
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

function enqueueToolTurn(text: string, tool: { id: string; name: string; args: string; }): void {
  scripts.push({
    first: text,
    rest: "",
    tool,
    markEntered: () => {},
    gate: Promise.resolve(),
    release: () => {},
  });
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
    if (script.tool) {
      for (const f of anthropicToolFrames(script.first, script.tool)) res.write(f);
      script.markEntered();
      res.end();
      return;
    }
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

test("a sibling browser reloads mid-gate and resumes a tool turn without duplicating its prefix", async ({ admin, creds, browser, db }) => {
  const sessionID = await createChatSession(admin, agentID);
  // First model call: ALPHA text plus a real `code` tool_use. The runtime
  // executes the tool server-side; its continuation request picks up the
  // gated script below.
  enqueueToolTurn("ALPHA-tool-prefix ", {
    id: "toolu_e2e",
    name: "code",
    args: JSON.stringify({ code: 'return "E2E_TOOL_RESULT"' }),
  });
  // The continuation is gated before any delta: the turn stays in flight
  // with the ALPHA/tool prefix already committed.
  const gate = enqueueGate("", "BETA-tool-final");
  const send = admin.stream(`/api/agents/${agentID}/sessions/${sessionID}/messages`, {
    parts: [{ type: "text", text: "run the tool" }],
  });
  try {
    await gate.entered;

    // Canonical proof before the reload: ALPHA, the tool call, and its real
    // result all live in ctx_message — the observe log alone would not prove
    // the prefix is reloadable history.
    await expect
      .poll(
        async () => {
          const rows = (await db.unsafe(
            `select m.role, m.event_type, m.content
             from ctx_message m
             join ctx_conversation c on c.id = m.conversation_id
             where c.session_id = $1`,
            [sessionID],
          )) as { role: string; event_type: string; content: string; }[];
          const alpha = rows.some((r) => r.role === "assistant" && r.content.includes("ALPHA-tool-prefix"));
          const call = rows.some((r) => r.event_type === "tool_call" && r.content.includes("toolu_e2e"));
          const result = rows.some((r) => r.role === "tool" && r.content.includes("E2E_TOOL_RESULT"));
          return alpha && call && result;
        },
        { timeout: 30_000, message: "canonical history holds ALPHA plus the tool call/result before reload" },
      )
      .toBe(true);

    const ctx = await browser.newContext({ baseURL: sibling.baseURL });
    const page = await ctx.newPage();
    try {
      await loginWithPassword(page, creds.admin.email, creds.admin.password);
      await page.goto(`/agents/${agentID}/sessions/${sessionID}`);
      // Mid-gate: committed text and the executed tool row are live on the
      // sibling while the primary's continuation call is still held.
      await expect(page.getByText("ALPHA-tool-prefix")).toBeVisible({ timeout: 30_000 });
      await expect(page.getByText('return "E2E_TOOL_RESULT"')).toBeVisible({ timeout: 30_000 });

      // Reload inside the gate: a cold start has no pin, so the observe
      // stream re-reads the open turn — history capped at the turn's
      // boundary plus a durable replay of the prefix onto an empty
      // container. ALPHA and the tool row must each appear exactly once.
      await page.reload();
      await expect(page.getByText("ALPHA-tool-prefix")).toBeVisible({ timeout: 30_000 });
      // The Stop button only renders once the resumed stream put the SDK
      // into streaming state — hydrate alone proves nothing about replay.
      await expect(page.getByRole("button", { name: "Stop", exact: true })).toBeVisible({ timeout: 30_000 });
      await expect(page.getByText('return "E2E_TOOL_RESULT"')).toBeVisible({ timeout: 30_000 });
      await expect(page.getByText("ALPHA-tool-prefix")).toHaveCount(1);
      await expect(page.getByText('return "E2E_TOOL_RESULT"')).toHaveCount(1);

      gate.release();
      await expect(page.getByText("BETA-tool-final")).toBeVisible({ timeout: 30_000 });
      await expect(page.getByText("ALPHA-tool-prefix")).toHaveCount(1);
    } finally {
      await ctx.close();
    }
  } finally {
    gate.release();
  }
  await send;
  await waitForRunState(db, sessionID, ["completed"], "run completes after the gated tool turn");
});

// W2: a browser send only registers the new turn's scope — it must not fire
// the boundary-capped history query that would merge the canonical user row
// over the optimistic one. The owned window counts GET /messages carrying
// snapshot_seq: that query only exists while a resume boundary is set, and a
// send must not set one.
test("a browser send does not re-query history or duplicate the user bubble", async ({ admin, creds, browser, db }) => {
  const sessionID = await createChatSession(admin, agentID);
  // Settle one turn first so the session carries canonical history the
  // post-send query would otherwise merge over the optimistic message.
  await admin.stream(`/api/agents/${agentID}/sessions/${sessionID}/messages`, {
    parts: [{ type: "text", text: "seeded first turn" }],
  });
  await waitForRunState(db, sessionID, ["completed"], "seeded turn completes");

  const ctx = await browser.newContext();
  const page = await ctx.newPage();
  const gate = enqueueGate("SEND-turn-alpha ", "SEND-turn-final");
  try {
    await loginWithPassword(page, creds.admin.email, creds.admin.password);
    await page.goto(`/agents/${agentID}/sessions/${sessionID}`);
    // Wait for the seeded turn's canonical transcript to be applied — the
    // heading only proves the shell mounted, and filling the composer before
    // hydration/draft restore settles races the send.
    await expect(page.getByText("e2e default reply")).toBeVisible({ timeout: 30_000 });
    const composer = page.getByPlaceholder("Message…");
    const sendButton = page.getByRole("button", { name: "Send message" });

    // Pure observation — no interception, so teardown has nothing pending.
    const cappedHistoryGets: string[] = [];
    const postSends: string[] = [];
    page.on("request", (req) => {
      const url = req.url();
      if (!url.includes(`/api/agents/${agentID}/sessions/${sessionID}/messages`)) return;
      if (req.method() === "POST") postSends.push(url);
      // Only the boundary-capped query is the regression surface: ordinary
      // uncapped history refetches are legitimate background traffic.
      if (req.method() === "GET" && url.includes("snapshot_seq=")) cappedHistoryGets.push(url);
    });

    await composer.fill("second turn from the UI");
    await expect(sendButton).toBeEnabled({ timeout: 10_000 });
    await composer.press("Enter");
    console.log("[w2] send submitted");
    // The browser POST must actually leave the page before we wait on the
    // model gate — an unsent Enter would stall here.
    await expect
      .poll(() => postSends.length, { timeout: 15_000, message: "browser POST /messages fired" })
      .toBe(1);
    await Promise.race([
      gate.entered,
      new Promise((_, rej) => setTimeout(() => rej(new Error("gate.entered timed out — POST never started the turn")), 30_000)),
    ]);
    console.log("[w2] provider call entered (POST reached the turn)");

    // Mid-turn: the optimistic bubble is the only copy, and the send fired
    // no boundary-capped history query — the resume boundary belongs to GET
    // observe alone.
    await expect(page.getByText("SEND-turn-alpha")).toBeVisible({ timeout: 30_000 });
    expect(cappedHistoryGets).toHaveLength(0);
    await expect(page.getByText("second turn from the UI")).toHaveCount(1);
    console.log("[w2] mid-turn assertions done");

    gate.release();
    await expect(page.getByText(/SEND-turn-final/)).toBeVisible({ timeout: 30_000 });
    await waitForRunState(db, sessionID, ["completed"], "send turn completes");
    // Canonical reconcile after the durable terminal still leaves one copy.
    await expect(page.getByText("second turn from the UI")).toHaveCount(1);
    console.log("[w2] post-completion assertions done");
  } finally {
    gate.release();
    console.log("[w2] closing page");
    await page.close().catch(() => {});
    await ctx.close().catch(() => {});
  }
});

// W3: a transient prefix failure is retried under the same pinned scope; a
// later non-retryable answer overwrites the recovery state instead of
// inheriting it, so a 403 stops the poll instead of looping forever. Events
// GETs are the observe coordinate — the bootstrap prefix is one of several
// history requests carrying snapshot_seq, so only the observe stream's
// retry/freeze is asserted.
test("a transient resume prefix failure retries the same scope; a 403 stops it", async ({ admin, creds, browser, db }) => {
  const sessionID = await createChatSession(admin, agentID);
  const gate = enqueueGate("RETRY-alpha ", "RETRY-beta");
  const send = admin.stream(`/api/agents/${agentID}/sessions/${sessionID}/messages`, {
    parts: [{ type: "text", text: "hold the gate" }],
  });
  const ctx = await browser.newContext();
  const ctx2 = await browser.newContext();
  try {
    // A bounded wait: a send that resolved non-200 never starts the turn, and
    // an unbounded await here would hide that behind the test timeout.
    await Promise.race([
      gate.entered,
      new Promise((_, rej) => setTimeout(() => rej(new Error("gate.entered timed out — turn never started")), 30_000)),
    ]);

    const page = await ctx.newPage();
    const eventsGets: { url: string; lastEventID: string | null; }[] = [];
    await page.route(`**/api/agents/${agentID}/sessions/${sessionID}/events**`, (route) => {
      eventsGets.push({
        url: route.request().url(),
        lastEventID: route.request().headers()["last-event-id"] ?? null,
      });
      return route.continue();
    });
    // Fail the resume bootstrap's canonical prefix (snapshot_seq requests)
    // until the test releases it — then pass through to the real handler.
    let prefix503 = true;
    const prefixFailures: number[] = [];
    await page.route(`**/api/agents/${agentID}/sessions/${sessionID}/messages?**`, (route) => {
      const req = route.request();
      if (req.method() !== "GET" || !req.url().includes("snapshot_seq=")) return route.continue();
      if (prefix503) {
        prefixFailures.push(503);
        return route.fulfill({ status: 503, body: "boom" });
      }
      return route.continue();
    });

    await loginWithPassword(page, creds.admin.email, creds.admin.password);
    await page.goto(`/agents/${agentID}/sessions/${sessionID}`);
    console.log("[w3] page loaded");

    // First observe: events 200, prefix 503 — a retryable failure, not gone.
    await expect
      .poll(() => prefixFailures.length, { timeout: 15_000, message: "first prefix attempt fails" })
      .toBeGreaterThanOrEqual(1);
    console.log("[w3] first 503 served");
    prefix503 = false;
    // The poll recovers, re-observes the SAME pinned scope, and the live
    // turn's stream attaches — Stop only renders in streaming state.
    await expect(page.getByText("RETRY-alpha")).toBeVisible({ timeout: 30_000 });
    await expect(page.getByRole("button", { name: "Stop", exact: true })).toBeVisible({
      timeout: 30_000,
    });
    console.log("[w3] resumed stream attached");
    expect(eventsGets.length).toBeGreaterThanOrEqual(2);
    const pins = eventsGets.map((r) => r.lastEventID).filter((v): v is string => v !== null);
    expect(pins.length).toBeGreaterThanOrEqual(1);
    expect(new Set(pins.map((p) => p.replace(/:0$/, ""))).size).toBe(1);
    expect(pins.every((p) => p.endsWith(":0"))).toBe(true);

    // The reverse transition in a fresh page session: 503 arms recovery, then
    // the next observe's prefix answers 403 — a fact, not an outage. Its
    // verdict must overwrite the retryable state, so the poll stops
    // re-observing.
    const page2 = await ctx2.newPage();
    const events2: string[] = [];
    await page2.route(`**/api/agents/${agentID}/sessions/${sessionID}/events**`, (route) => {
      events2.push(route.request().url());
      return route.continue();
    });
    // Failure phases are keyed to OBSERVE rounds, not history request counts:
    // while fewer than two events GETs have run, every capped history request
    // (bootstrap prefix or React Query refetch alike) answers 503. From the
    // second observe on they answer 403.
    const prefix2Statuses: number[] = [];
    await page2.route(`**/api/agents/${agentID}/sessions/${sessionID}/messages?**`, (route) => {
      const req = route.request();
      if (req.method() !== "GET" || !req.url().includes("snapshot_seq=")) return route.continue();
      const status = events2.length < 2 ? 503 : 403;
      prefix2Statuses.push(status);
      return route.fulfill({ status, body: "boom" });
    });
    await loginWithPassword(page2, creds.admin.email, creds.admin.password);
    await page2.goto(`/agents/${agentID}/sessions/${sessionID}`);
    // The second observe really reached its prefix under 403: the new verdict
    // overwrote recovery state, so two full poll periods (3s each) pass with
    // no further events request.
    await expect
      .poll(() => prefix2Statuses.includes(403), {
        timeout: 15_000,
        message: "the retried observe's prefix answered 403",
      })
      .toBe(true);
    console.log("[w3] second observe hit prefix 403");
    const eventsFrozen = events2.length;
    await page2.waitForTimeout(7_000);
    expect(events2.length).toBe(eventsFrozen);
    console.log("[w3] events GET frozen for 7s");

    // Unblock the model before closing: a held gate keeps a real SSE response
    // in flight, which makes page/context close wait forever.
    gate.release();
    await send;
    await waitForRunState(db, sessionID, ["completed"], "run completes after the gate releases");
    console.log("[w3] run completed, closing contexts");
    await page2.close().catch(() => {});
    await ctx2.close().catch(() => {});
    await page.close().catch(() => {});
    await ctx.close().catch(() => {});
    console.log("[w3] contexts closed");
  } finally {
    gate.release();
    await ctx2.close().catch(() => {});
    await ctx.close().catch(() => {});
  }
});
