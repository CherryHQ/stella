import { type ClassValue, clsx } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]): string {
  return twMerge(clsx(inputs));
}

/**
 * Extract a human-readable message from an unknown thrown value: the
 * `instanceof Error` message when available, otherwise the value's string form.
 * This is the only place an untyped catch is narrowed; call sites never cast.
 */
export function errorMessage<T>(input: T): string {
  return input instanceof Error ? input.message : String(input);
}

// SAFETY: every HTML event-handler target is the originating element, whose value
// is the string the user typed; this is the single boundary that reads it.
export function targetValue(e: { target: { value?: string } }): string {
  return e.target.value ?? "";
}
