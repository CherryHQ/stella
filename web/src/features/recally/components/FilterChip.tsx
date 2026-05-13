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
      className={`rounded-full border px-2 py-1 text-xs transition-colors ${
        active
          ? "border-input bg-accent font-medium text-foreground"
          : "border-border bg-background text-muted-foreground hover:bg-accent/50 hover:text-foreground"
      }`}
    >
      {label}
    </button>
  );
}
