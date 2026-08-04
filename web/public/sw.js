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
    event.respondWith(cacheFirst(request));
    return;
  }

  if (request.mode === "navigate") {
    event.respondWith(shellNetworkFirst(request));
  }
});

function cacheFirst(request) {
  return self.caches.match(request).then((hit) => {
    if (hit) return hit;
    return self.fetch(request).then((response) => {
      if (response.ok) {
        const copy = response.clone();
        void self.caches.open(CACHE).then((cache) => cache.put(request, copy));
      }
      return response;
    });
  });
}

// Network-first so a server upgrade lands on the next load. Every SPA route
// returns the same document, so any successful navigation refreshes the
// offline shell.
function shellNetworkFirst(request) {
  return self
    .fetch(request)
    .then((response) => {
      // A logged-out visitor is bounced to /login, and a redirected response
      // cannot be cached.
      if (response.ok && !response.redirected) {
        const copy = response.clone();
        void self.caches.open(CACHE).then((cache) => cache.put(SHELL_KEY, copy));
      }
      return response;
    })
    .catch(() => self.caches.match(SHELL_KEY).then((hit) => hit || Response.error()));
}
