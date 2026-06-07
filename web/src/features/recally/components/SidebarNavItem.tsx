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
      className={`flex w-full items-center justify-between mx-1 rounded-lg px-2.5 py-1.5 text-[12px] transition-colors duration-120 cursor-pointer ${
        active
          ? "bg-accent text-accent-foreground font-semibold"
          : "text-muted-foreground hover:bg-muted hover:text-foreground"
      }`}
    >
      <span>{label}</span>
      {count !== undefined && (
        <span className="text-[10px] font-mono text-muted-foreground tabular-nums">{count}</span>
      )}
    </button>
  );
}
