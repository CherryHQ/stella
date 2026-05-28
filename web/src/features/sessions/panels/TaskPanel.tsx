// PR 3 (task system v2) deleted the v1 task API. This panel needs to be
// rewritten against the new flat /api/tasks routes; tracked as a follow-up
// PR (web migration / Phase 6.5).

export function TaskPanel() {
  return (
    <div className="p-6 text-sm text-muted-foreground">
      The task panel is being migrated for task system v2. See PR 3.
    </div>
  );
}
