import { keepPreviousData, queryOptions } from "@tanstack/react-query";
import {
  getDeliverable,
  getDeliverableReadiness,
  listAcceptanceEvents,
  listAttempts,
  listDeliverableChildren,
  listDeliverables,
  listEdges,
  listRevisions,
} from "@/lib/api-client";
import type { ComponentsDeliverable } from "@/lib/api-client/types.gen";
import { fetchAllDeliverables, fetchAllSubtree, offsetPageToken } from "@/lib/paginated";

// deliverablesOptions fetches every root (all pages) for callers that aggregate
// over the whole set (e.g. the overview hub). The list page uses the
// server-paginated deliverablesPageOptions instead.
export function deliverablesOptions(agentId: string, archived = false) {
  return queryOptions({
    queryKey: ["deliverables", agentId, archived ? "archived" : "active"],
    queryFn: async () => fetchAllDeliverables(agentId, archived),
    enabled: !!agentId,
  });
}

export const DELIVERABLES_PAGE_SIZE = 24;

export interface DeliverablesPageParams {
  agentId: string;
  archived?: boolean;
  /** undefined = any; false = active (non-terminal); true = terminal (history). */
  terminal?: boolean;
  /** exact lifecycle; undefined = all lifecycles in scope. */
  lifecycle?: string;
  /** case-insensitive substring on title/intent; server-side. */
  q?: string;
  /** 1-based page. */
  page?: number;
}

export interface DeliverablesPage {
  deliverables: ComponentsDeliverable[];
  total: number;
}

// deliverablesPageOptions drives the list page from one server page: filtering,
// search, and pagination all run in the DB so the first paint no longer waits
// for every deliverable to download.
export function deliverablesPageOptions(p: DeliverablesPageParams) {
  const page = Math.max(1, p.page ?? 1);
  return queryOptions({
    queryKey: [
      "deliverables-page",
      p.agentId,
      p.archived ?? false,
      p.terminal ?? null,
      p.lifecycle ?? "",
      p.q ?? "",
      page,
    ],
    queryFn: async (): Promise<DeliverablesPage> => {
      const { data } = await listDeliverables({
        query: {
          agent_id: p.agentId,
          archived: p.archived || undefined,
          terminal: p.terminal,
          lifecycle: p.lifecycle || undefined,
          q: p.q || undefined,
          page_size: DELIVERABLES_PAGE_SIZE,
          page_token: offsetPageToken((page - 1) * DELIVERABLES_PAGE_SIZE),
        },
        throwOnError: true,
      });
      return { deliverables: data?.deliverables ?? [], total: data?.total ?? 0 };
    },
    enabled: !!p.agentId,
    // Keep the previous page's data (and its total) on screen while the next
    // page loads, so the pager's total never transiently drops to 0.
    placeholderData: keepPreviousData,
  });
}

async function deliverablesCount(agentId: string, q: { archived?: boolean; terminal?: boolean }) {
  const { data } = await listDeliverables({
    query: { agent_id: agentId, ...q, page_size: 1 },
    throwOnError: true,
  });
  return data?.total ?? 0;
}

// deliverableCountsOptions powers the active/history/archived header badges with
// cheap server-side counts, independent of the current page or filters.
export function deliverableCountsOptions(agentId: string) {
  return queryOptions({
    queryKey: ["deliverables-counts", agentId],
    queryFn: async () => {
      const [active, history, archived] = await Promise.all([
        deliverablesCount(agentId, { terminal: false }),
        deliverablesCount(agentId, { terminal: true }),
        deliverablesCount(agentId, { archived: true }),
      ]);
      return { active, history, archived };
    },
    enabled: !!agentId,
  });
}

export function deliverableOptions(deliverableId: string) {
  return queryOptions({
    queryKey: ["deliverable", deliverableId],
    queryFn: async () => {
      const { data } = await getDeliverable({
        path: { id: deliverableId },
        throwOnError: true,
      });
      return data as ComponentsDeliverable;
    },
    enabled: !!deliverableId,
  });
}

/** Direct children of a composite, in position order. */
export function deliverableChildrenOptions(deliverableId: string | undefined) {
  return queryOptions({
    queryKey: ["deliverable-children", deliverableId],
    queryFn: async () => {
      const { data } = await listDeliverableChildren({
        path: { id: deliverableId! },
        throwOnError: true,
      });
      return data?.deliverables ?? [];
    },
    enabled: !!deliverableId,
  });
}

/** Every deliverable in the tree (the root_id family) for graph/tree views. */
export function deliverableSubtreeOptions(rootId: string | undefined) {
  return queryOptions({
    queryKey: ["deliverable-subtree", rootId],
    queryFn: async () => fetchAllSubtree(rootId!),
    enabled: !!rootId,
  });
}

export function deliverableReadinessOptions(deliverableId: string | undefined) {
  return queryOptions({
    queryKey: ["deliverable-readiness", deliverableId],
    queryFn: async () => {
      const { data } = await getDeliverableReadiness({
        path: { id: deliverableId! },
        throwOnError: true,
      });
      return data ?? null;
    },
    enabled: !!deliverableId,
  });
}

export function deliverableAttemptsOptions(deliverableId: string | undefined) {
  return queryOptions({
    queryKey: ["deliverable-attempts", deliverableId],
    queryFn: async () => {
      const { data } = await listAttempts({
        path: { id: deliverableId! },
        throwOnError: true,
      });
      return data?.attempts ?? [];
    },
    enabled: !!deliverableId,
  });
}

export function deliverableEventsOptions(deliverableId: string | undefined) {
  return queryOptions({
    queryKey: ["deliverable-events", deliverableId],
    queryFn: async () => {
      const { data } = await listAcceptanceEvents({
        path: { id: deliverableId! },
        throwOnError: true,
      });
      return data?.acceptance_events ?? [];
    },
    enabled: !!deliverableId,
  });
}

export function deliverableEdgesOptions(deliverableId: string | undefined) {
  return queryOptions({
    queryKey: ["deliverable-edges", deliverableId],
    queryFn: async () => {
      const { data } = await listEdges({
        path: { id: deliverableId! },
        throwOnError: true,
      });
      return data?.edges ?? [];
    },
    enabled: !!deliverableId,
  });
}

export function deliverableRevisionsOptions(deliverableId: string | undefined) {
  return queryOptions({
    queryKey: ["deliverable-revisions", deliverableId],
    queryFn: async () => {
      const { data } = await listRevisions({
        path: { id: deliverableId! },
        throwOnError: true,
      });
      return data?.revisions ?? [];
    },
    enabled: !!deliverableId,
  });
}
