import type { ReactNode } from "react";

export function SidebarNavItem({
  icon,
  label,
  count,
  active,
  onClick,
}: {
  icon?: ReactNode;
  label: string;
  count?: number;
  active: boolean;
  onClick: () => void;
}) {
  return (
    <button
      onClick={onClick}
      className={`flex w-full items-center gap-2 mx-1 rounded-lg px-2.5 py-1.5 text-[12px] transition-colors duration-120 cursor-pointer ${
        active
          ? "bg-accent text-accent-foreground font-semibold"
          : "text-muted-foreground hover:bg-muted hover:text-foreground"
      }`}
    >
      {icon && <span className="shrink-0">{icon}</span>}
      <span className="flex-1 truncate text-left">{label}</span>
      {count !== undefined && (
        <span className="text-xs font-mono text-muted-foreground tabular-nums">{count}</span>
      )}
    </button>
  );
}
