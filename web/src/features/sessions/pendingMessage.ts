/**
 * A first message handed from the page that created a session to the session
 * page that will send it.
 *
 * The text can be arbitrarily long, so it never travels in the URL; it is
 * parked here for the one navigation hop and claimed exactly once — a reload of
 * the thread must not re-send it.
 */
const pending = new Map<string, string>();

export function stashPendingMessage(sessionId: string, text: string) {
  if (!sessionId || !text.trim()) return;
  pending.set(sessionId, text);
}

export function takePendingMessage(sessionId: string): string | undefined {
  const text = pending.get(sessionId);
  if (text !== undefined) pending.delete(sessionId);
  return text;
}
