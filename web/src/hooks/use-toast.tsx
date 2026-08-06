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

/** Subscribe to the queue. Exported for {@link ToastContainer} and for tests. */
export function subscribeToasts(listener: () => void) {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

/** Current queue, by reference — stable between emits, as `useSyncExternalStore` requires. */
export function getToasts(): ToastMsg[] {
  return toasts;
}

function dismiss(id: number) {
  toasts = toasts.filter((t) => t.id !== id);
  emit();
}

/**
 * Publish a toast. This is the store, and it is plain — no React — so the thing
 * the app depends on can be tested without a DOM, and so a caller outside a
 * component (an error handler, a query callback) can raise one at all.
 */
export function showToast(
  text: string,
  kind: ToastMsg["kind"] = "success",
  timeout = TOAST_TIMEOUT_MS,
) {
  const id = ++seq;
  toasts = [...toasts, { id, text, kind }];
  emit();
  setTimeout(() => dismiss(id), timeout);
}

const TOAST_TIMEOUT_MS = 3000;

/** Component-facing sugar over {@link showToast}, keeping the existing call shape. */
export function useToast(timeout = TOAST_TIMEOUT_MS) {
  const show = useCallback(
    (text: string, kind: ToastMsg["kind"] = "success") => showToast(text, kind, timeout),
    [timeout],
  );

  return { showToast: show } as const;
}

/**
 * Mounted once, at the root. Elevation carries the surface and the icon carries
 * the status, so severity survives both a color-blind reader and a screen
 * reader. A solid `bg-*` fill is deliberately not used: a status token and its
 * `-foreground` share a hue, so filling with one and writing in the other makes
 * the text vanish — which is exactly what this component used to do.
 */
export function ToastContainer() {
  const messages = useSyncExternalStore(subscribeToasts, getToasts, getToasts);
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
