import type { MessageKey } from "@/lib/i18n/messages";
import type { Session } from "@/lib/types";

/**
 * How a thread got here, when that is not obvious.
 *
 * A thread list answers "what is this" and "when", and stays silent about the
 * one thing a person cannot infer: whether they typed it, a webhook posted it,
 * or a schedule started it. Two real fields already carry that — `kind` and
 * `channel` — and neither is surfaced anywhere in a list.
 *
 * Deliberately absent on ordinary web chats. In a live deployment 18 of 25
 * sessions are `kind: "chat"` over `channel: "web"`; a chip on every row would
 * be decoration, and a signal that is always present signals nothing. This
 * returns null for the default case so the chip means "something unusual
 * happened here".
 */

/** Kinds that describe an origin rather than a plain conversation. */
const KIND_LABELS: Record<string, MessageKey> = {
  main: "sessions.origin.main",
  scheduler: "sessions.origin.scheduler",
  task: "sessions.origin.goal",
  delegate: "sessions.origin.delegate",
};

/** Channels with a proper name of their own. Anything else passes through. */
const CHANNEL_LABELS: Record<string, MessageKey> = {
  webhook: "sessions.origin.webhook",
  telegram: "sessions.origin.telegram",
  feishu: "sessions.origin.feishu",
  qq: "sessions.origin.qq",
  wechat: "sessions.origin.wechat",
  weixin: "sessions.origin.wechat",
  slack: "sessions.origin.slack",
  discord: "sessions.origin.discord",
  email: "sessions.origin.email",
};

/** Typing a message in the browser is the default; it needs no label. */
const DEFAULT_CHANNEL = "web";

type Translate = (key: MessageKey) => string;

/**
 * A ready-to-render origin label, or null when the thread arrived the ordinary
 * way. Callers do not need to know that this comes from two different fields.
 */
export function sessionOriginLabel(
  session: Pick<Session, "kind" | "channel">,
  t: Translate,
): string | null {
  const kindLabel = KIND_LABELS[session.kind ?? ""];
  if (kindLabel) return t(kindLabel);

  const channel = (session.channel ?? "").trim();
  if (!channel || channel === DEFAULT_CHANNEL) return null;

  // Channel-bound sessions carry a composite address (`<agent>:user:<id>:private`),
  // which is an internal routing key, not something to show a person. Only the
  // `:channel:<name>` form names a real channel.
  const name = channel.includes(":") ? (channel.match(/:channel:([^:]+)/)?.[1] ?? "") : channel;
  if (!name || name === DEFAULT_CHANNEL) return null;

  const known = CHANNEL_LABELS[name.toLowerCase()];
  return known ? t(known) : name;
}
