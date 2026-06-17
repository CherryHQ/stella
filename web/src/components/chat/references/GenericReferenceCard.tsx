import { FileQuestion } from "lucide-react";
import type { RenderableReference } from "@/lib/types";
import { ReferenceCardShell } from "./ReferenceCardShell";

/**
 * Fallback for reference types this client build doesn't know how to render
 * (e.g. a newer backend emitting a type the UI predates). Shows what we have —
 * type + preview title + short id — and offers no Open target.
 */
export function GenericReferenceCard({ reference }: { reference: RenderableReference }) {
  return (
    <ReferenceCardShell
      icon={FileQuestion}
      kind={reference.type}
      title={reference.preview?.title ?? reference.id.slice(0, 8)}
      muted
    />
  );
}
