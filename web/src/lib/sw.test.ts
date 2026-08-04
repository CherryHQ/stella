import { readFileSync } from "node:fs";
import { describe, expect, it, vi } from "vitest";

// public/sw.js is a classic service worker, so it cannot be imported. It reads
// every capability off `self`, which lets us run it against a stub scope.
const source = readFileSync(new URL("../../public/sw.js", import.meta.url), "utf8");

const ORIGIN = "https://stella.test";

interface StubRequest {
  method: string;
  url: string;
  mode: string;
}

interface StubResponse {
  ok: boolean;
  redirected: boolean;
  body: string;
  clone: () => StubResponse;
}

function request(path: string, overrides: Partial<StubRequest> = {}): StubRequest {
  return { method: "GET", url: `${ORIGIN}${path}`, mode: "cors", ...overrides };
}

function response(body: string, overrides: Partial<StubResponse> = {}): StubResponse {
  const stub: StubResponse = {
    ok: true,
    redirected: false,
    body,
    clone: () => stub,
    ...overrides,
  };
  return stub;
}

function cacheKey(key: StubRequest | string): string {
  return typeof key === "string" ? key : new URL(key.url).pathname;
}

function startWorker(networkResponse: (req: StubRequest | string) => Promise<StubResponse>) {
  const listeners = new Map<string, (event: unknown) => void>();
  const stored = new Map<string, StubResponse>();
  const fetch = vi.fn((req: StubRequest | string) => networkResponse(req));

  const cache = {
    put: (key: StubRequest | string, res: StubResponse) => {
      stored.set(cacheKey(key), res);
      return Promise.resolve();
    },
  };

  const scope = {
    location: { origin: ORIGIN },
    addEventListener: (type: string, fn: (event: unknown) => void) => listeners.set(type, fn),
    skipWaiting: () => Promise.resolve(),
    clients: { claim: () => Promise.resolve() },
    fetch,
    caches: {
      open: () => Promise.resolve(cache),
      match: (key: StubRequest | string) => Promise.resolve(stored.get(cacheKey(key))),
      keys: () => Promise.resolve([...new Set(["stella-shell-v1", "stella-shell-v0"])]),
      delete: (key: string) => Promise.resolve(key !== "stella-shell-v1"),
    },
  };

  // eslint-disable-next-line @typescript-eslint/no-implied-eval
  new Function("self", source)(scope);

  return {
    stored,
    fetch,
    /** Returns the response the worker took over with, or undefined if it passed through. */
    handle(req: StubRequest): Promise<StubResponse> | undefined {
      let taken: Promise<StubResponse> | undefined;
      listeners.get("fetch")?.({
        request: req,
        respondWith: (value: Promise<StubResponse>) => {
          taken = value;
        },
      });
      return taken;
    },
  };
}

const offline = () => Promise.reject(new Error("offline"));

describe("service worker routing", () => {
  it("leaves every server-owned path to the browser", () => {
    const worker = startWorker(() => Promise.resolve(response("unused")));

    for (const path of [
      "/api/agents/a/sessions/s/chat",
      "/api-references",
      "/auth/login/github",
      "/oauth/authorize",
      "/webhooks/abc",
      "/static/docs/x.png",
      "/healthz",
      "/readyz",
    ]) {
      expect(worker.handle(request(path)), path).toBeUndefined();
    }
    expect(worker.fetch).not.toHaveBeenCalled();
  });

  it("leaves navigations to a server-owned path alone", () => {
    const worker = startWorker(() => Promise.resolve(response("scalar")));
    expect(worker.handle(request("/api-references", { mode: "navigate" }))).toBeUndefined();
  });

  it("leaves non-GET and cross-origin requests alone", () => {
    const worker = startWorker(() => Promise.resolve(response("unused")));

    expect(worker.handle(request("/assets/app-abc.js", { method: "POST" }))).toBeUndefined();
    expect(
      worker.handle({ method: "GET", url: "https://cdn.example.com/assets/x.js", mode: "cors" }),
    ).toBeUndefined();
  });

  it("caches hashed assets on first use and serves later hits offline", async () => {
    const worker = startWorker(() => Promise.resolve(response("chunk")));
    const asset = request("/assets/route-abc123.js");

    await expect(worker.handle(asset)).resolves.toMatchObject({ body: "chunk" });
    expect(worker.fetch).toHaveBeenCalledTimes(1);

    const cached = startWorker(offline);
    cached.stored.set("/assets/route-abc123.js", response("chunk"));
    await expect(cached.handle(asset)).resolves.toMatchObject({ body: "chunk" });
    expect(cached.fetch).not.toHaveBeenCalled();
  });

  it("does not cache a failed asset response", async () => {
    const worker = startWorker(() => Promise.resolve(response("gone", { ok: false })));

    await worker.handle(request("/assets/route-abc123.js"));
    expect(worker.stored.has("/assets/route-abc123.js")).toBe(false);
  });

  it("prefers the network for navigations and refreshes the shell", async () => {
    const worker = startWorker(() => Promise.resolve(response("fresh shell")));

    await expect(worker.handle(request("/agents", { mode: "navigate" }))).resolves.toMatchObject({
      body: "fresh shell",
    });
    expect(worker.stored.get("/index.html")).toMatchObject({ body: "fresh shell" });
  });

  it("falls back to the cached shell when the network is gone", async () => {
    const worker = startWorker(offline);
    worker.stored.set("/index.html", response("cached shell"));

    await expect(worker.handle(request("/agents", { mode: "navigate" }))).resolves.toMatchObject({
      body: "cached shell",
    });
  });

  it("never stores a redirected navigation as the shell", async () => {
    const worker = startWorker(() => Promise.resolve(response("/login", { redirected: true })));

    await worker.handle(request("/", { mode: "navigate" }));
    expect(worker.stored.has("/index.html")).toBe(false);
  });

  it("ignores non-navigation requests it does not own", () => {
    const worker = startWorker(() => Promise.resolve(response("unused")));
    expect(worker.handle(request("/favicon.svg"))).toBeUndefined();
  });
});
