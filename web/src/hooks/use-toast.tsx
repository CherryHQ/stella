import { useCallback, useSyncExternalStore } from "react";
import { AlertCircle, CheckCircle2 } from "lucide-react";

export interface ToastMsg {
  id: number;
  text: string;
  kind: "success" | "error";
}

/**
 * Toasts live in a module store, not in component state, and exactly one
 * {@link ToastContainer} renders them (mounted at the router root).
 *
 * The per-component `useState` this replaces meant a child's `showToast` wrote
 * to a different instance than the parent's container read from, so every toast
 * raised by a detail panel while its list page held the container was silently
 * dropped — providers, users, and the new-provider form never showed one.
 */
let toasts: ToastMsg[] = [];
let seq = 0;
const listeners = new Set<() => void>();

function emit() {
  for (const l of listeners) l();
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

function dismiss(id: number) {
  toasts = toasts.filter((t) => t.id !== id);
  emit();
}

export function useToast(timeout = 3000) {
  const showToast = useCallback(
    (text: string, kind: ToastMsg["kind"] = "success") => {
      const id = ++seq;
      toasts = [...toasts, { id, text, kind }];
      emit();
      setTimeout(() => dismiss(id), timeout);
    },
    [timeout],
  );

  return { showToast } as const;
}

/**
 * Mounted once, at the root. Elevation carries the surface and the icon carries
 * the status, so severity survives both a color-blind reader and a screen
 * reader. A solid `bg-*` fill is deliberately not used: a status token and its
 * `-foreground` share a hue, so filling with one and writing in the other makes
 * the text vanish — which is exactly what this component used to do.
 */
export function ToastContainer() {
  const messages = useSyncExternalStore(
    subscribe,
    () => toasts,
    () => toasts,
  );
  if (messages.length === 0) return null;
  return (
    <div
      role="status"
      aria-live="polite"
      className="pointer-events-none fixed bottom-4 right-4 z-[9999] flex flex-col gap-2"
    >
      {messages.map((m) => (
        <div
          key={m.id}
          className="pointer-events-auto flex items-center gap-2 rounded-lg border border-border bg-popover px-3.5 py-2.5 text-sm text-popover-foreground shadow-lg"
        >
          {m.kind === "error" ? (
            <AlertCircle size={16} className="shrink-0 text-destructive-foreground" />
          ) : (
            <CheckCircle2 size={16} className="shrink-0 text-success" />
          )}
          {m.text}
        </div>
      ))}
    </div>
  );
}
