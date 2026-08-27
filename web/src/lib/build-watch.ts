import { client } from "@/lib/api-client/client.gen";

// Kept in sync with server.BuildHeader.
const HEADER = "X-Stella-Build";

// The build this page load is running, taken from the first stamped response
// the tab ever sees. Module scope is exactly one page load, which is the right
// lifetime for a baseline that no component should be able to reset.
let bootBuild: string | null = null;

// Replaced rather than mutated: useSyncExternalStore compares snapshots by
// identity, so a stable object is what stops it from re-rendering forever.
interface BuildSnapshot {
  stale: boolean;
  build: string | null;
}

let snapshot: BuildSnapshot = { stale: false, build: null };

const listeners = new Set<() => void>();

/**
 * Starts watching the build the server reports.
 *
 * Every response carries it, so an upgrade is noticed from traffic the app
 * already makes — no timer, no extra request, and no session resolution or
 * admin-only status work on a five-minute loop per open tab.
 *
 * Watching the service worker instead would never fire: sw.js is the same bytes
 * in every release, so the browser sees no worker update to install. What
 * actually goes stale is the loaded bundle.
 */
export function watchBuild(): void {
  client.interceptors.response.use((response) => {
    observeBuild(response.headers.get(HEADER));
    return response;
  });
}

// Exported for tests. A rollback puts the server back on the tab's own build,
// and the notice correctly disappears — hence a comparison rather than a latch.
export function observeBuild(build: string | null): void {
  if (!build) return;
  if (bootBuild === null) bootBuild = build;

  const stale = build !== bootBuild;
  if (stale === snapshot.stale && build === snapshot.build) return;

  snapshot = { stale, build };
  for (const listener of listeners) listener();
}

export function subscribeToBuild(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

export function buildSnapshot(): BuildSnapshot {
  return snapshot;
}

// Test-only: module state outlives a single test otherwise.
export function resetBuildWatchForTest(): void {
  bootBuild = null;
  snapshot = { stale: false, build: null };
  listeners.clear();
}
