import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

export function SettingsListHeader({ title, action }: { title: ReactNode; action?: ReactNode }) {
  return (
    <div className="flex min-h-12 items-center justify-between gap-3 border-b border-border px-4 py-3">
      <span className="font-mono text-[10px] font-semibold uppercase tracking-[0.14em] text-muted-foreground/70">
        {title}
      </span>
      {action && <div className="shrink-0">{action}</div>}
    </div>
  );
}

export function SettingsListItem({
  active,
  children,
  className,
  onClick,
}: {
  active?: boolean;
  children: ReactNode;
  className?: string;
  onClick?: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "w-full rounded-xl px-3 py-2.5 text-left transition-colors",
        active
          ? "bg-accent text-accent-foreground"
          : "text-foreground/85 hover:bg-foreground/[0.045] hover:text-foreground",
        className,
      )}
    >
      {children}
    </button>
  );
}

export function SettingsListBody({ children }: { children: ReactNode }) {
  return <div className="space-y-1 p-2">{children}</div>;
}
