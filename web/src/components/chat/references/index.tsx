import type { RenderableReference } from "@/lib/types";
import { ArticleReferenceCard } from "./ArticleReferenceCard";
import { GenericReferenceCard } from "./GenericReferenceCard";
import { GoalReferenceCard } from "./GoalReferenceCard";

/**
 * Per-type card registry. Each renderer owns its own hydration and Open target;
 * the registry only maps a reference `type` to its card. Unknown types fall back
 * to {@link GenericReferenceCard}. Goals and tasks are both goals now, so
 * the legacy `task`/`goal` types resolve to the same card.
 */
const registry = new Map([
  ["goal", GoalReferenceCard],
  ["task", GoalReferenceCard],
  ["recally_article", ArticleReferenceCard],
]);

/**
 * Renders the renderable-reference cards an agent emitted in a tool step. Placed
 * by {@link StepsGroup} outside the collapsible so cards are always visible, not
 * buried in the (default-collapsed) raw tool output. Dedupes by type+id within
 * this list (one StepsGroup); the same entity surfacing in a different step
 * group still renders once per group.
 */
export function RenderableReferenceList({ references }: { references: RenderableReference[] }) {
  const seen = new Set<string>();
  const unique = references.filter((r) => {
    const key = `${r.type}:${r.id}`;
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
  if (unique.length === 0) return null;

  return (
    <div className="flex max-w-xl flex-col gap-2">
      {unique.map((reference) => {
        const Card = registry.get(reference.type) ?? GenericReferenceCard;
        return <Card key={`${reference.type}:${reference.id}`} reference={reference} />;
      })}
    </div>
  );
}
