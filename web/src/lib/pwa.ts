// Progressive-web-app wiring for the SPA entry point. The worker itself lives
// in public/sw.js so the browser can serve it from the origin root.

const RELOAD_MARK_KEY = "stella:chunk-reload";
const RELOAD_COOLDOWN_MS = 60_000;

/**
 * Installs the offline app shell, making the Web UI installable.
 *
 * No-ops in dev, where a worker caching Vite's assets would fight HMR, and
 * outside secure contexts, where `navigator.serviceWorker` is undefined — a
 * deployment served over plain HTTP simply stays a regular SPA.
 */
export function registerServiceWorker(): void {
  if (!import.meta.env.PROD) return;
  const browserWindow = globalThis.window;
  if (!browserWindow || !("serviceWorker" in navigator)) return;

  browserWindow.addEventListener("load", () => {
    void navigator.serviceWorker.register("/sw.js").catch(() => {});
  });
}

/**
 * Recovers a long-lived tab from a server upgrade.
 *
 * Route chunks are content-hashed, so once the server ships a new build the
 * chunk names this tab knows are gone and the first navigation into a route it
 * has not visited yet fails. Reload to pick up the new build, at most once per
 * cooldown so a genuinely broken build cannot spin.
 */
export function recoverFromStaleChunks(): void {
  const browserWindow = globalThis.window;
  if (!browserWindow) return;

  browserWindow.addEventListener("vite:preloadError", (event) => {
    const last = Number(sessionStorage.getItem(RELOAD_MARK_KEY) ?? 0);
    if (Date.now() - last < RELOAD_COOLDOWN_MS) return;

    sessionStorage.setItem(RELOAD_MARK_KEY, String(Date.now()));
    event.preventDefault();
    browserWindow.location.reload();
  });
}
