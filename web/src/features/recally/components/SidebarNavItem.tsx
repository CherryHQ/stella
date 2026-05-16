export function SidebarNavItem({
  label,
  count,
  active,
  onClick,
}: {
  label: string;
  count?: number;
  active: boolean;
  onClick: () => void;
}) {
  return (
    <button
      onClick={onClick}
      className={`flex w-full items-center justify-between mx-1 rounded-lg px-2.5 py-1.5 text-[12px] transition-all duration-150 ${
        active
          ? "bg-sidebar-accent font-medium text-foreground"
          : "text-foreground/80 hover:bg-muted/50"
      }`}
    >
      <span>{label}</span>
      {count !== undefined && (
        <span className="text-[10px] font-mono text-muted-foreground/50 tabular-nums">{count}</span>
      )}
    </button>
  );
}
