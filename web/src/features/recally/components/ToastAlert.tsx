export function ToastAlert({
  toast,
}: {
  toast: { message: string; type: "success" | "error" } | null;
}) {
  if (!toast) return null;
  return (
    <div
      className={`fixed bottom-4 right-4 z-50 w-auto max-w-sm rounded-xl border px-4 py-3 shadow-none text-sm font-medium ${
        toast.type === "error"
          ? "border-destructive/20 bg-destructive/10 text-destructive-foreground"
          : "border-success/20 bg-success/10 text-success-foreground"
      }`}
    >
      {toast.message}
    </div>
  );
}
