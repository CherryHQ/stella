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

/**
 * Trailing `"` is optional so the truncated final pair still matches, and the
 * optional trailing `\` keeps a cut that landed on a backslash from dropping the
 * whole pair — `"message":"done \` would otherwise match nothing at all.
 */
const PAIR = /"([A-Za-z_][\w.-]*)"\s*:\s*"((?:[^"\\]|\\.)*\\?)(?:"|$)/g;

/** Keys that name what happened, most specific first. */
const LABEL_KEYS = ["event", "event_type", "type", "action", "name", "subject", "title"];

/** Keys that carry the human-readable body. */
const BODY_KEYS = ["message", "text", "body", "content", "summary", "description", "prompt"];

const ESCAPES = new Map([
  ["n", "\n"],
  ["r", "\r"],
  ["t", "\t"],
  ["b", "\b"],
  ["f", "\f"],
  ['"', '"'],
  ["\\", "\\"],
  ["/", "/"],
]);

/**
 * Undo JSON string escaping.
 *
 * Hand-rolling this over a fixed set of escapes gets `\uXXXX` wrong, and
 * `\uXXXX` is not exotic: Python's `json.dumps` emits it by default, so any
 * non-ASCII webhook body arrives escaped and a hand-rolled pass renders the
 * title as literal `部署`. It also mangles a legitimately escaped
 * backslash. So let `JSON.parse` do it — it is the reference implementation of
 * exactly this grammar.
 *
 * The catch is why this module exists at all: the title was cut to 60 chars, so
 * the value can end mid-escape and fail to parse. Shave the tail one character
 * at a time (an escape is at most six) before giving up on a lenient scan, which
 * also covers a payload that was never valid JSON to begin with.
 */
function unescape(value: string): string {
  for (let end = value.length; end >= 0 && end >= value.length - 6; end--) {
    try {
      // SAFETY: the JSON fragment is a quoted string serialized above.
      return JSON.parse(`"${value.slice(0, end)}"`) as string;
    } catch {
      // Shorter, then.
    }
  }

  let out = "";
  for (let i = 0; i < value.length; i++) {
    if (value[i] !== "\\") {
      out += value[i];
      continue;
    }
    const next = value[++i];
    if (next === undefined) break; // dangling backslash: the cut landed here
    if (next === "u") {
      const hex = value.slice(i + 1, i + 5);
      if (!/^[0-9a-fA-F]{4}$/.test(hex)) break; // truncated escape ends the value
      // Per code unit, so a surrogate pair reassembles on its own.
      out += String.fromCharCode(parseInt(hex, 16));
      i += 4;
      continue;
    }
    out += ESCAPES.get(next) ?? next;
  }
  return out;
}

function decode(value: string): string {
  return unescape(value).replace(/\s+/g, " ").trim();
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
    // Lowercased to match the backend, which compares keys case-insensitively:
    // a sender using `{"Event": ...}` should get the same title from both.
    // First occurrence wins: outer keys are the ones the sender led with.
    const name = key.toLowerCase();
    if (clean && !pairs.has(name)) pairs.set(name, clean);
  }
  if (pairs.size === 0) return raw;

  const label = pick(pairs, LABEL_KEYS);
  const body = pick(pairs, BODY_KEYS);
  if (label && body) return `${label} · ${body}`;
  // SAFETY: label or body was set, and both are strings.
  if (label || body)
    // SAFETY: label or body was set, and both are strings.
    return (label ?? body) as string;

  // Nothing recognizable, but the pairs still beat raw JSON.
  return [...pairs.values()].slice(0, 2).join(" · ");
}
