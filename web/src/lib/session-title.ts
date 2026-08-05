/**
 * Readable titles for machine-authored sessions.
 *
 * The backend names a session after its first message, truncated to 60
 * characters (`autoTitle`, internal/agent/runtime/chat.go). That is fine for a
 * person typing a sentence and useless for a webhook posting a JSON body: the
 * thread list fills with `{"event":"smoke_test","message":"Verify webh…`.
 *
 * The important detail is that those titles do not parse. Truncation cuts the
 * last value mid-string, so every JSON title in a live deployment fails
 * `JSON.parse` — a parse-based reader would silently do nothing to all of them.
 * This reads key/value pairs out of the fragment instead, and tolerates the
 * unterminated final one.
 *
 * Display-time only, so it fixes sessions that already exist. Titling the
 * session better at the source would only help new ones.
 */

/** Trailing `"` is optional so the truncated final pair still matches. */
const PAIR = /"([A-Za-z_][\w.-]*)"\s*:\s*"((?:[^"\\]|\\.)*)(?:"|$)/g;

/** Keys that name what happened, most specific first. */
const LABEL_KEYS = ["event", "event_type", "type", "action", "name", "subject", "title"];

/** Keys that carry the human-readable body. */
const BODY_KEYS = ["message", "text", "body", "content", "summary", "description", "prompt"];

function decode(value: string): string {
  return value
    .replace(/\\n|\\r|\\t/g, " ")
    .replace(/\\(["\\/])/g, "$1")
    .replace(/\s+/g, " ")
    .trim();
}

function pick(pairs: Map<string, string>, keys: string[]): string | undefined {
  for (const key of keys) {
    const found = pairs.get(key);
    if (found) return found;
  }
  return undefined;
}

/**
 * A title a person can read. Returns `fallback` for an untitled session and the
 * original string for anything that is already prose.
 */
export function sessionDisplayTitle(title: string | null | undefined, fallback: string): string {
  const raw = (title ?? "").trim();
  if (!raw) return fallback;
  if (!raw.startsWith("{") && !raw.startsWith("[")) return raw;

  const pairs = new Map<string, string>();
  for (const [, key, value] of raw.matchAll(PAIR)) {
    const clean = decode(value);
    // First occurrence wins: outer keys are the ones the sender led with.
    if (clean && !pairs.has(key)) pairs.set(key, clean);
  }
  if (pairs.size === 0) return raw;

  const label = pick(pairs, LABEL_KEYS);
  const body = pick(pairs, BODY_KEYS);
  if (label && body) return `${label} · ${body}`;
  if (label || body) return (label ?? body) as string;

  // Nothing recognizable, but the pairs still beat raw JSON.
  return [...pairs.values()].slice(0, 2).join(" · ");
}
