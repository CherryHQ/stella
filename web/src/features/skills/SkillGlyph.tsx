import { Blocks } from "lucide-react";
import { cn } from "@/lib/utils";

/** Placeholder mark for a skill — shared by the inspector header and market cards. */
export function SkillGlyph({ className }: { className?: string }) {
  return (
    <div
      className={cn(
        "flex size-9 shrink-0 items-center justify-center rounded-lg border bg-card text-muted-foreground",
        className,
      )}
    >
      <Blocks className="size-5" />
    </div>
  );
}
