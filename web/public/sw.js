/**
 * Stella's offline app shell.
 *
 * Registered by src/lib/pwa.ts. Vite copies public/ to the root of
 * static/dist/, which Go serves at "/", so this worker's scope is the whole
 * origin — hence the explicit list of paths the Go server owns.
 *
 * Every capability is reached through `self`, so sw.test.ts can drive the
 * routing policy against a stub scope.
 */

// One cache for the life of the deployment. Hashed assets from superseded
// builds are never pruned, which trades a slow storage drift the browser can
// evict for not having to ship a precache manifest; bump the version, or prune
// from a manifest, if that growth ever matters.
const CACHE = "stella-shell-v1";

// Cache key for the offline shell, not a path to fetch: "/" redirects to /login
// or /providers, and Go's file server sends "/index.html" back to "/", so
// neither can be primed at install time. Every SPA route returns the same
// document, so the first navigation this worker controls seeds the shell —
// which means offline support starts on the second visit.
const SHELL_KEY = "/index.html";

// Root paths owned by the Go server rather than the SPA build. Chat replies
// stream over SSE, so touching /api/ at all risks buffering a live response.
// Keep in sync with internal/server/routes.go.
const SERVER_PREFIXES = [
  "/api/",
  "/api-references",
  "/auth/",
  "/oauth/",
  "/webhooks/",
  "/static/",
  "/healthz",
  "/readyz",
];

self.addEventListener("install", (event) => {
  event.waitUntil(self.skipWaiting());
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    self.caches
      .keys()
      .then((keys) =>
        Promise.all(keys.filter((key) => key !== CACHE).map((key) => self.caches.delete(key))),
      )
      .then(() => self.clients.claim()),
  );
});

self.addEventListener("fetch", (event) => {
  const request = event.request;
  if (request.method !== "GET") return;

  const url = new URL(request.url);
  if (url.origin !== self.location.origin) return;
  if (SERVER_PREFIXES.some((prefix) => url.pathname.startsWith(prefix))) return;

  // Build output is content-hashed and served immutable, so a hit is never
  // stale and no precache manifest is needed.
  if (url.pathname.startsWith("/assets/")) {
    event.respondWith(cacheFirst(event));
    return;
  }

  if (request.mode === "navigate") {
    event.respondWith(shellNetworkFirst(event));
  }
});

function cacheFirst(event) {
  const request = event.request;
  return self.caches.match(request).then((hit) => {
    if (hit) return hit;
    return self.fetch(request).then((response) => {
      // A reverse proxy doing SPA fallback answers a vanished hashed chunk with
      // the app shell and a 200. Storing that under a script URL is
      // unrecoverable: the entry never expires, and because the hashes are
      // deterministic, rolling the server back to the build that owns the URL
      // still reads HTML out of this cache. The Go handler 404s these, but
      // nothing guarantees what sits in front of it.
      if (isStorable(response) && !isHTML(response)) {
        store(event, request, response.clone());
      }
      return response;
    });
  });
}

// Network-first so a server upgrade lands on the next load. Every SPA route
// returns the same document, so any successful navigation refreshes the
// offline shell.
//
// Deliberate ceiling: no timeout. On a connection that is degraded rather than
// dead the navigation waits on the network instead of falling back to the
// cached shell. Add a race against a timer if that becomes the common
// complaint; a timeout that fires too eagerly serves a stale shell to someone
// who was about to get the real page.
function shellNetworkFirst(event) {
  return self
    .fetch(event.request)
    .then((response) => {
      if (isStorable(response)) {
        store(event, SHELL_KEY, response.clone());
      }
      return response;
    })
    .catch(() => self.caches.match(SHELL_KEY).then((hit) => hit || Response.error()));
}

// A logged-out visitor is bounced to /login, and a redirected response cannot be
// cached at all.
function isStorable(response) {
  return response.ok && !response.redirected;
}

function isHTML(response) {
  return (response.headers.get("Content-Type") || "").startsWith("text/html");
}

// Cache writes must outlive the response: once respondWith settles the browser
// is free to kill the worker, and a half-written entry means the next offline
// launch is missing the very thing this visit was supposed to store. A failed
// write (quota, eviction) must never fail the fetch.
function store(event, key, response) {
  event.waitUntil(
    self.caches
      .open(CACHE)
      .then((cache) => cache.put(key, response))
      .catch(() => {}),
  );
}
