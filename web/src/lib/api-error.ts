// apiErrorMessage extracts a human-readable message from a thrown API error.
// With throwOnError the generated client rejects with the parsed error body
// ({ error: { message } }) rather than an Error instance, so fall back through
// both shapes before the caller's fallback string.
export function apiErrorMessage(err: unknown, fallback: string): string {
  // SAFETY: with throwOnError the client rejects with the parsed API error body.
  const apiMessage = (err as { error?: { message?: unknown } })?.error?.message;
  if (typeof apiMessage === "string" && apiMessage) return apiMessage;
  if (err instanceof Error && err.message) return err.message;
  return fallback;
}
