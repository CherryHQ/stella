import type { JsonValue } from "./types";

function isText(value: JsonValue | undefined): value is string {
  return typeof value === "string";
}

function isNumber(value: JsonValue | undefined): value is number {
  return typeof value === "number";
}

// apiErrorMessage extracts a human-readable message from a thrown API error.
// With throwOnError the generated client rejects with the parsed error body
// ({ error: { message } }) rather than an Error instance, so fall back through
// both shapes before the caller's fallback string.
export function apiErrorMessage<TError>(err: TError, fallback: string): string {
  // SAFETY: with throwOnError the client rejects with the parsed API error body.
  const apiMessage = (err as { error?: { message?: JsonValue } })?.error?.message;
  if (isText(apiMessage) && apiMessage) return apiMessage;
  if (err instanceof Error && err.message) return err.message;
  return fallback;
}

// apiErrorCode returns the HTTP status the API error body carries
// ({ error: { code } }), or undefined for anything that is not an API error.
export function apiErrorCode<TError>(err: TError): number | undefined {
  // SAFETY: with throwOnError the client rejects with the parsed API error body.
  const code = (err as { error?: { code?: JsonValue } })?.error?.code;
  return isNumber(code) ? code : undefined;
}
