import type { RenderableReference } from "@/lib/types";
import { GenericReferenceCard } from "./GenericReferenceCard";
import { TaskReferenceCard } from "./TaskReferenceCard";

/**
 * Per-type card registry. Each renderer owns its own hydration and Open target;
 * the registry only maps a reference `type` to its card. Unknown types fall back
 * to {@link GenericReferenceCard}. Add goal / article renderers here in P2.
 */
const registry: Record<string, React.ComponentType<{ reference: RenderableReference }>> = {
  task: TaskReferenceCard,
};

/**
 * Renders the renderable-reference cards an agent emitted in a tool step. Placed
 * by {@link StepsGroup} outside the collapsible so cards are always visible, not
 * buried in the (default-collapsed) raw tool output. Dedupes by type+id so a
 * reference echoed across steps shows once.
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
    <div className="flex flex-col gap-2">
      {unique.map((reference) => {
        const Card = registry[reference.type] ?? GenericReferenceCard;
        return <Card key={`${reference.type}:${reference.id}`} reference={reference} />;
      })}
    </div>
  );
}
