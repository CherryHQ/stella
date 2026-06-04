export function FilterChip({
  label,
  active,
  onClick,
}: {
  label: string;
  active: boolean;
  onClick: () => void;
}) {
  return (
    <button
      onClick={onClick}
      className={`rounded-full px-3 py-0.5 border text-xs font-mono transition-colors duration-120 cursor-pointer ${
        active
          ? "bg-primary/10 text-primary border-primary/20 font-semibold"
          : "bg-card border-border text-muted-foreground hover:bg-muted hover:text-foreground"
      }`}
    >
      {label}
    </button>
  );
}
