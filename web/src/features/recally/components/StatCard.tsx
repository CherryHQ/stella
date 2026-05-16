export function StatCard({ value, label }: { value: number; label: string }) {
  return (
    <div className="flex items-baseline gap-1.5">
      <span className="font-mono text-sm font-semibold tabular-nums text-foreground">{value}</span>
      <span className="text-[10px] font-mono uppercase tracking-wider text-muted-foreground/60">
        {label}
      </span>
    </div>
  );
}
