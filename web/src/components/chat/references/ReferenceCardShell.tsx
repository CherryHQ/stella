import type { LucideIcon } from "lucide-react";
import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

/**
 * Shared layout for every renderable-reference card. Cards differ only in their
 * icon, kind label, status node, and Open target; this shell owns the common
 * frame so task / goal / article cards stay visually identical (design V2).
 */
export function ReferenceCardShell({
  icon: Icon,
  kind,
  title,
  status,
  action,
  muted,
}: {
  icon: LucideIcon;
  kind: string;
  title: ReactNode;
  status?: ReactNode;
  action?: ReactNode;
  /** Dim the frame for tombstone / unknown states. */
  muted?: boolean;
}) {
  return (
    <div
      className={cn(
        "flex items-center gap-3 rounded-xl border border-border bg-card px-3 py-2.5 text-sm",
        muted && "opacity-70",
      )}
    >
      <span className="grid size-7 shrink-0 place-items-center rounded-lg bg-muted text-muted-foreground">
        <Icon className="size-4" />
      </span>
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <div className="flex items-center gap-2">
          <span className="font-mono text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">
            {kind}
          </span>
          {status}
        </div>
        <span className="truncate font-medium text-foreground">{title}</span>
      </div>
      {action}
    </div>
  );
}
