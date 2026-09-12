import { Chat } from "@ai-sdk/react";
import type { UIMessage } from "ai";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  createSessionTransport,
  ResumeSupersededError,
  type SessionResumeMeta,
} from "./chat-transport";

// A minimal SSE body: bootstrap headers ride the response, the body carries
// the Vercel UI-message chunks the SDK parses.
function sse(frames: object[]): Response {
  const body = frames.map((f) => `data: ${JSON.stringify(f)}\n\n`).join("") + "data: [DONE]\n\n";
  return new Response(body, { status: 200 });
}

const STREAM_FRAMES = (scope: string) => [
  { type: "start", messageId: `msg-${scope}` },
  { type: "text-start", id: `${scope}:t:2` },
  { type: "text-delta", id: `${scope}:t:2`, delta: "ALPHA" },
  { type: "text-end", id: `${scope}:t:2` },
  { type: "finish" },
];

const BOOTSTRAP_HEADERS = (scope: string, boundary = 3) => ({
  "x-stella-history-before": String(boundary),
  "x-stella-message-id": `msg-${scope}`,
});

afterEach(() => vi.unstubAllGlobals());

describe("createSessionTransport observe pinning", () => {
  it("sends the pinned scope as Last-Event-ID <scope>:0", async () => {
    const seen: (HeadersInit | undefined)[] = [];
    vi.stubGlobal("fetch", async (_url: string, init?: RequestInit) => {
      seen.push(init?.headers);
      return sse(STREAM_FRAMES("run-1"));
    });
    const transport = createSessionTransport("a1", "s1", {
      observeScope: () => "run-1",
      onResumeReady: () => Promise.resolve(),
    });
    const stream = await transport.reconnectToStream({ chatId: "s1" });
    expect(stream).not.toBeNull();
    expect(new Headers(seen[0]).get("Last-Event-ID")).toBe("run-1:0");
  });

  it.each([500, 503, 403])(
    "a %i bootstrap response is a read failure, never a release",
    async (status) => {
      const gone = vi.fn();
      vi.stubGlobal("fetch", async () => new Response("boom", { status }));
      const transport = createSessionTransport("a1", "s1", {
        observeScope: () => "run-1",
        onResumeGone: gone,
      });
      await expect(transport.reconnectToStream({ chatId: "s1" })).rejects.toThrow();
      expect(gone).not.toHaveBeenCalled();
    },
  );

  it.each([204, 410])(
    "an explicit %i releases only the scope this request carried",
    async (status) => {
      const gone = vi.fn();
      vi.stubGlobal("fetch", async () => new Response(null, { status }));
      const transport = createSessionTransport("a1", "s1", {
        observeScope: () => "run-1",
        onResumeGone: gone,
      });
      if (status === 204) {
        expect(await transport.reconnectToStream({ chatId: "s1" })).toBeNull();
      } else {
        await expect(transport.reconnectToStream({ chatId: "s1" })).rejects.toThrow();
      }
      expect(gone).toHaveBeenCalledWith("run-1");
    },
  );

  it("never releases a pin the request did not carry", async () => {
    const gone = vi.fn();
    vi.stubGlobal("fetch", async () => new Response(null, { status: 204 }));
    const transport = createSessionTransport("a1", "s1", {
      observeScope: () => undefined,
      onResumeGone: gone,
    });
    expect(await transport.reconnectToStream({ chatId: "s1" })).toBeNull();
    expect(gone).not.toHaveBeenCalled();
  });

  it("a rejected bootstrap aborts the observe connection and propagates", async () => {
    let signal: AbortSignal | undefined;
    vi.stubGlobal("fetch", async (_url: string, init?: RequestInit) => {
      signal = init?.signal ?? undefined;
      const res = sse(STREAM_FRAMES("run-1"));
      return new Response(res.body, {
        status: 200,
        headers: BOOTSTRAP_HEADERS("run-1"),
      });
    });
    const transport = createSessionTransport("a1", "s1", {
      onResumeReady: () => Promise.reject(new Error("stale session resume")),
    });
    await expect(transport.reconnectToStream({ chatId: "s1" })).rejects.toThrow(
      "stale session resume",
    );
    expect(signal?.aborted).toBe(true);
  });

  it("a superseded bootstrap resolves the reconnect as null, not an error", async () => {
    const gone = vi.fn();
    vi.stubGlobal(
      "fetch",
      async () =>
        new Response(
          STREAM_FRAMES("run-1")
            .map((f) => `data: ${JSON.stringify(f)}\n\n`)
            .join("") + "data: [DONE]\n\n",
          { status: 200, headers: BOOTSTRAP_HEADERS("run-1") },
        ),
    );
    const transport = createSessionTransport("a1", "s1", {
      // A newer send/observe already retired this request — the hook signals
      // supersede, and the SDK must see "nothing to resume".
      onResumeReady: () => Promise.reject(new ResumeSupersededError("stale session resume")),
      onResumeGone: gone,
    });
    expect(await transport.reconnectToStream({ chatId: "s1" })).toBeNull();
    // The synthetic answer is transport-internal: it must not release the
    // pin through the gone path.
    expect(gone).not.toHaveBeenCalled();
  });

  it("a send superseding an in-flight observe resolves it as null", async () => {
    let observeSignal: AbortSignal | undefined;
    vi.stubGlobal("fetch", async (url: string, init?: RequestInit) => {
      if (String(url).includes("/events")) {
        observeSignal = init?.signal ?? undefined;
        // The observe hangs until its request is retired by the send.
        return new Promise<Response>((_resolve, reject) => {
          init?.signal?.addEventListener("abort", () =>
            reject(new DOMException("aborted", "AbortError")),
          );
        });
      }
      return new Response(
        STREAM_FRAMES("run-9")
          .map((f) => `data: ${JSON.stringify(f)}\n\n`)
          .join("") + "data: [DONE]\n\n",
        { status: 200, headers: BOOTSTRAP_HEADERS("run-9") },
      );
    });
    const sendReady = vi.fn();
    const transport = createSessionTransport("a1", "s1", { onSendReady: sendReady });
    const reconnect = transport.reconnectToStream({ chatId: "s1" });
    // Let the observe request issue, then a send supersedes it.
    await Promise.resolve();
    await Promise.resolve();
    const send = await transport.sendMessages({
      trigger: "submit-message",
      chatId: "s1",
      messageId: undefined,
      messages: [{ id: "u1", role: "user", parts: [{ type: "text", text: "hi" }] }],
      abortSignal: undefined,
    });
    expect(send).not.toBeNull();
    expect(observeSignal?.aborted).toBe(true);
    await expect(reconnect).resolves.toBeNull();
    expect(sendReady).toHaveBeenCalled();
  });

  it.each([500, 503, 403])(
    "a %i observe failure reports the request scope for retry triage",
    async (status) => {
      const failed = vi.fn();
      vi.stubGlobal("fetch", async () => new Response("boom", { status }));
      const transport = createSessionTransport("a1", "s1", {
        observeScope: () => "run-1",
        onResumeFailed: failed,
      });
      await expect(transport.reconnectToStream({ chatId: "s1" })).rejects.toThrow();
      expect(failed).toHaveBeenCalledWith("run-1", status);
    },
  );

  it("every observe failure reports its own status — a later 403 supersedes a 503", async () => {
    const failed = vi.fn();
    const statuses = [503, 403];
    vi.stubGlobal("fetch", async () => new Response("boom", { status: statuses.shift() }));
    const transport = createSessionTransport("a1", "s1", {
      observeScope: () => "run-1",
      onResumeFailed: failed,
    });
    await expect(transport.reconnectToStream({ chatId: "s1" })).rejects.toThrow();
    await expect(transport.reconnectToStream({ chatId: "s1" })).rejects.toThrow();
    // Each failure is classified on its own: the verdict the page applies is
    // always the last failure's, so a non-retryable answer overwrites an
    // earlier retryable one instead of inheriting it.
    expect(failed.mock.calls).toEqual([
      ["run-1", 503],
      ["run-1", 403],
    ]);
  });

  it("a dropped observe connection reports a retryable failure, not gone", async () => {
    const failed = vi.fn();
    const gone = vi.fn();
    vi.stubGlobal("fetch", async () => {
      throw new TypeError("network down");
    });
    const transport = createSessionTransport("a1", "s1", {
      observeScope: () => "run-1",
      onResumeFailed: failed,
      onResumeGone: gone,
    });
    await expect(transport.reconnectToStream({ chatId: "s1" })).rejects.toThrow();
    expect(failed).toHaveBeenCalledWith("run-1", 0);
    expect(gone).not.toHaveBeenCalled();
  });

  it("a response racing in after the request was superseded resolves null", async () => {
    const gone = vi.fn();
    const failed = vi.fn();
    const ready = vi.fn();
    let issued!: () => void;
    const issuedPromise = new Promise<void>((r) => {
      issued = r;
    });
    // fetch resolves normally even though its signal already aborted — the
    // transport must still treat the request as retired: no seeding, no
    // Gone/Failed callbacks against the live pin.
    vi.stubGlobal("fetch", async () => {
      issued();
      await new Promise((r) => setTimeout(r, 0));
      return new Response(
        STREAM_FRAMES("run-1")
          .map((f) => `data: ${JSON.stringify(f)}\n\n`)
          .join("") + "data: [DONE]\n\n",
        { status: 200, headers: BOOTSTRAP_HEADERS("run-1") },
      );
    });
    const transport = createSessionTransport("a1", "s1", {
      observeScope: () => "run-1",
      onResumeReady: ready,
      onResumeGone: gone,
      onResumeFailed: failed,
    });
    const reconnect = transport.reconnectToStream({ chatId: "s1" });
    // Retire the observe only after its request actually issued — close()
    // before the controller exists would abort nothing.
    await issuedPromise;
    transport.close();
    await expect(reconnect).resolves.toBeNull();
    expect(ready).not.toHaveBeenCalled();
    expect(gone).not.toHaveBeenCalled();
    expect(failed).not.toHaveBeenCalled();
  });

  it("a send response registers the turn identity without a resume reseed", async () => {
    const sendReady = vi.fn();
    const resumeReady = vi.fn();
    // The send response carries the same bootstrap headers the real handler
    // attaches to a streamed turn.
    vi.stubGlobal("fetch", async (url: string) =>
      String(url).includes("/messages")
        ? new Response(
            STREAM_FRAMES("run-9")
              .map((f) => `data: ${JSON.stringify(f)}\n\n`)
              .join("") + "data: [DONE]\n\n",
            { status: 200, headers: BOOTSTRAP_HEADERS("run-9") },
          )
        : new Response(null, { status: 204 }),
    );
    const transport = createSessionTransport("a1", "s1", {
      onSendReady: sendReady,
      onResumeReady: resumeReady,
    });
    const stream = await transport.sendMessages({
      trigger: "submit-message",
      chatId: "s1",
      messageId: undefined,
      messages: [{ id: "u1", role: "user", parts: [{ type: "text", text: "hi" }] }],
      abortSignal: undefined,
    });
    expect(stream).not.toBeNull();
    const meta: SessionResumeMeta = { historyBefore: 3, messageId: "msg-run-9" };
    expect(sendReady).toHaveBeenCalledWith(meta);
    expect(resumeReady).not.toHaveBeenCalled();
  });
});

describe("real Chat resume through the transport", () => {
  it("replays the pinned turn onto the seeded container exactly once", async () => {
    const scope = "run-1";
    const received: { type?: string; data?: unknown }[] = [];
    const finishes: string[] = [];
    vi.stubGlobal("fetch", async (url: string) => {
      if (!String(url).includes("/events")) {
        return new Response(null, { status: 404 });
      }
      return new Response(
        [
          { type: "start", messageId: `msg-${scope}` },
          { type: "text-start", id: `${scope}:t:2` },
          { type: "text-delta", id: `${scope}:t:2`, delta: "ALPHA" },
          { type: "text-end", id: `${scope}:t:2` },
          {
            type: "data-turn-terminal",
            transient: true,
            data: { session_id: "s1", scope, result: "completed", reason: "" },
          },
          { type: "finish" },
        ]
          .map((f) => `data: ${JSON.stringify(f)}\n\n`)
          .join("") + "data: [DONE]\n\n",
        { status: 200, headers: BOOTSTRAP_HEADERS(scope) },
      );
    });

    // History shape at reload: previous turn (user+assistant), the current
    // turn's user message, and the current turn's already-hydrated overlay —
    // text plus a materialized tool part the replay must not double.
    const prevUser: UIMessage = {
      id: "u-prev",
      role: "user",
      parts: [{ type: "text", text: "earlier" }],
    };
    const prevAssistant: UIMessage = {
      id: "prev-assistant",
      role: "assistant",
      parts: [{ type: "text", text: "PREVIOUS ANSWER" }],
    };
    const curUser: UIMessage = { id: "u-cur", role: "user", parts: [{ type: "text", text: "hi" }] };
    const staleOverlay: UIMessage = {
      id: "e-overlay",
      role: "assistant",
      parts: [
        { type: "text", text: "ALPHA" },
        {
          type: "dynamic-tool",
          toolName: "code",
          toolCallId: "toolu_1",
          state: "output-available",
          input: {},
          output: "E2E_TOOL_RESULT",
        } as UIMessage["parts"][number],
      ],
    };
    let chat!: Chat<UIMessage>;
    const transport = createSessionTransport("a1", "s1", {
      observeScope: () => scope,
      // The hook's seed: the canonical prefix through the boundary (previous
      // turn + this turn's user input) plus the stream's empty container —
      // before the SDK snapshots lastMessage for the replay.
      onResumeReady: (meta) => {
        chat.messages = [
          prevUser,
          prevAssistant,
          curUser,
          { id: meta.messageId, role: "assistant", parts: [] },
        ];
        return Promise.resolve();
      },
    });
    chat = new Chat<UIMessage>({
      id: "s1",
      transport,
      messages: [prevUser, prevAssistant, curUser, staleOverlay],
      onData: (part) => received.push(part),
      onFinish: ({ message }) => finishes.push(message.id),
    });

    await chat.resumeStream();

    const last = chat.lastMessage;
    expect(last?.id).toBe(`msg-${scope}`);
    const text = (last?.parts ?? [])
      .filter((p) => p.type === "text")
      .map((p) => p.text)
      .join("");
    expect(text).toBe("ALPHA");
    // The previous turn survives the seed — only the current turn's
    // materialized overlay is replaced by the replay container.
    expect(chat.messages.some((m) => m.id === "prev-assistant")).toBe(true);
    expect(chat.messages.some((m) => m.id === "e-overlay")).toBe(false);
    expect(received.some((p) => p.type === "data-turn-terminal")).toBe(true);
    expect(finishes).toEqual([`msg-${scope}`]);
  });

  it("a resume superseded mid-bootstrap by a same-chat send never stamps error", async () => {
    // The old observe is parked inside onResumeReady when a send on the same
    // Chat retires it. Releasing the gate late must resolve the stale request
    // as "nothing to resume" — the send's submitted/streaming status is the
    // truth, not the aborted request's outcome.
    let releaseBootstrap!: () => void;
    const bootstrapGate = new Promise<void>((r) => {
      releaseBootstrap = r;
    });
    let bootstrapEntered!: () => void;
    const entered = new Promise<void>((r) => {
      bootstrapEntered = r;
    });
    // The send stream opens and stays open: the SDK parks in streaming.
    let sendBody!: ReadableStreamDefaultController<Uint8Array>;
    const enc = new TextEncoder();
    vi.stubGlobal("fetch", async (url: string) => {
      if (String(url).includes("/messages")) {
        return new Response(
          new ReadableStream<Uint8Array>({
            start(c) {
              sendBody = c;
              c.enqueue(enc.encode('data: {"type":"start","messageId":"msg-run-9"}\n\n'));
            },
          }),
          { status: 200, headers: BOOTSTRAP_HEADERS("run-9") },
        );
      }
      return new Response(
        STREAM_FRAMES("run-1")
          .map((f) => `data: ${JSON.stringify(f)}\n\n`)
          .join("") + "data: [DONE]\n\n",
        { status: 200, headers: BOOTSTRAP_HEADERS("run-1") },
      );
    });
    const transport = createSessionTransport("a1", "s1", {
      observeScope: () => "run-1",
      onResumeReady: async (_meta, signal) => {
        bootstrapEntered();
        await bootstrapGate;
        // Same contract the hook keeps: a request retired while it waited is
        // superseded, not failed.
        if (signal.aborted) throw new ResumeSupersededError("stale session resume");
      },
    });
    const chat = new Chat<UIMessage>({ id: "s1", transport });

    const resume = chat.resumeStream();
    await entered; // the observe request is inside its bootstrap
    const send = chat.sendMessage({ text: "hi" });
    await vi.waitFor(() => expect(chat.status).toBe("streaming"));
    releaseBootstrap();
    await resume;
    // The send's stream is still open and untouched; the superseded resume
    // resolved as null without touching status.
    expect(chat.status).toBe("streaming");
    sendBody.close();
    await send;
  });
});
