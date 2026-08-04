import { useQuery } from "@tanstack/react-query";
import { statusQueryOptions } from "@/lib/queries/status";

// The build this page load is running, captured from the first status the tab
// ever sees. Module scope is the right lifetime: it is exactly one page load,
// so remounting the notice cannot lose the baseline the way a ref would.
let bootBuild: string | null = null;

/**
 * Reports whether the server has moved to a different build than this tab.
 *
 * Watching the service worker instead would never fire: sw.js is the same bytes
 * in every release, so the browser sees no update to install. What actually
 * goes stale is the loaded bundle, and the server version is the honest signal
 * for that. Client-side routing means a tab can otherwise run an old build for
 * days without a single navigation to correct it.
 */
export function useBuildUpdate(): { stale: boolean; build: string | null } {
  const { data } = useQuery(statusQueryOptions);

  const build = data ? `${data.version}@${data.commit ?? ""}` : null;
  if (build !== null && bootBuild === null) {
    bootBuild = build;
  }

  return { stale: build !== null && bootBuild !== null && build !== bootBuild, build };
}
