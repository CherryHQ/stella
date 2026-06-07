import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

export function SettingsListHeader({ title, action }: { title: ReactNode; action?: ReactNode }) {
  return (
    <div className="flex min-h-12 items-center justify-between gap-3 border-b border-border px-4 py-3">
      <span className="text-xs font-semibold text-muted-foreground">{title}</span>
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
        "w-full rounded-lg px-3 py-2.5 text-left transition-colors duration-120 cursor-pointer",
        active
          ? "bg-accent text-accent-foreground font-semibold"
          : "text-muted-foreground hover:bg-muted hover:text-foreground",
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
