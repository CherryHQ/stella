import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ArrowUpRight, ListTodo, Target } from "lucide-react";
import { useI18n } from "@/lib/i18n";
import type { RenderableReference } from "@/lib/types";
import { deliverableChildrenOptions, deliverableOptions } from "@/lib/queries/deliverables";
import {
  StatusPill,
  deliverableStatusLabel,
  displayStatus,
  rollup,
} from "@/features/deliverables/lib";
import { ReferenceCardShell } from "./ReferenceCardShell";

/** Lifecycles a deliverable can no longer move out of — polling stops here. */
const TERMINAL = new Set(["accepted", "rejected_final", "abandoned", "cancelled"]);
const POLL_MS = 15_000;

/**
 * Chat reference card for a deliverable — the merged successor to the old goal
 * and task cards (both are deliverables now). A composite (decomposed root or
 * sub-tree) reads as a Target with a children rollup; a leaf reads as a ListTodo
 * worker unit. Polls while the entity is non-terminal so a referenced run's
 * status stays live in an open conversation.
 */
export function DeliverableReferenceCard({ reference }: { reference: RenderableReference }) {
  const { t } = useI18n();
  const { data: deliverable, isError } = useQuery({
    ...deliverableOptions(reference.id),
    refetchInterval: (query) => (TERMINAL.has(query.state.data?.lifecycle ?? "") ? false : POLL_MS),
  });

  const isComposite = deliverable?.kind === "composite";
  // Children only matter for a composite's rollup line; skip the request for leaves.
  const { data: children } = useQuery({
    ...deliverableChildrenOptions(isComposite ? reference.id : undefined),
  });

  // Live entity wins; preview is only a first-paint placeholder, never trusted
  // for routing or existence.
  const title = deliverable?.title ?? reference.preview?.title ?? reference.id;
  const Icon = isComposite ? Target : ListTodo;

  if (isError) {
    return (
      <ReferenceCardShell
        icon={ListTodo}
        kind={t("references.deliverable")}
        title={t("references.deleted")}
        muted
      />
    );
  }

  const r = isComposite && children ? rollup(children) : null;

  return (
    <div className="flex flex-col gap-1">
      <ReferenceCardShell
        icon={Icon}
        kind={t("references.deliverable")}
        title={title}
        status={
          deliverable ? (
            <StatusPill
              status={displayStatus(deliverable)}
              label={deliverableStatusLabel(t, deliverable)}
            />
          ) : undefined
        }
        action={
          deliverable ? (
            <Link
              to="/agents/$agentId/deliverables/$deliverableId"
              params={{ agentId: deliverable.agent_id, deliverableId: reference.id }}
              className="inline-flex shrink-0 items-center gap-1 rounded-lg px-2 py-1 font-mono text-xs text-muted-foreground hover:bg-muted hover:text-foreground"
            >
              {t("references.open")}
              <ArrowUpRight className="size-3.5" />
            </Link>
          ) : undefined
        }
      />
      {r && r.total > 0 && (
        <div className="pl-12 text-[11px] text-muted-foreground/70">
          {t("deliverables.requiredOf", { accepted: r.accepted, total: r.total })}
        </div>
      )}
    </div>
  );
}
