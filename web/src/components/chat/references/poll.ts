/** Statuses a goal or task can no longer move out of. */
const TERMINAL = new Set(["done", "failed", "cancelled"]);

const POLL_MS = 15_000;

/**
 * `refetchInterval` for an entity card: poll every 15s while the entity is in a
 * non-terminal status, stop once it settles. Polling is inherently scoped to a
 * mounted card — TanStack pauses it when the last observer unmounts — so closed
 * conversations cost nothing. Typed structurally to stay agnostic of each
 * query's key/data generics.
 */
export function pollWhileActive(query: { state: { data?: { status?: string } } }): number | false {
  const status = query.state.data?.status;
  return status && TERMINAL.has(status) ? false : POLL_MS;
}
