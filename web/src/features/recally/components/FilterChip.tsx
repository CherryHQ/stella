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
      className={`rounded-full px-3 py-0.5 border text-xs font-mono transition-all duration-150 ${
        active
          ? "bg-primary/10 text-primary border-primary/20 font-semibold shadow-2xs"
          : "bg-muted/30 border-transparent text-muted-foreground/80 hover:bg-muted/70 hover:text-foreground"
      }`}
    >
      {label}
    </button>
  );
}
