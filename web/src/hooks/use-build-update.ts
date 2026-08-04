import { useSyncExternalStore } from "react";
import { buildSnapshot, subscribeToBuild } from "@/lib/build-watch";

/**
 * Reports whether the server has moved to a different build than this tab.
 *
 * The signal rides on API responses the app was making anyway; see
 * lib/build-watch.ts for why a poll was the wrong shape. Client-side routing
 * means a tab can otherwise run an old bundle for days without a single
 * navigation to correct it.
 */
export function useBuildUpdate(): { stale: boolean; build: string | null } {
  return useSyncExternalStore(subscribeToBuild, buildSnapshot, buildSnapshot);
}
