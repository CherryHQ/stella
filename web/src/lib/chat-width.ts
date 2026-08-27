/**
 * Width of the conversation column.
 *
 * A chat transcript and an agent session are not the same surface. Reading
 * prose wants a short measure; watching an agent work wants to see a diff, a
 * table, or 120-column tool output without horizontal scrolling. One number
 * cannot serve both, so it is a preference rather than a constant.
 *
 * Seven components have to agree on this width — the transcript, the composer,
 * the error notice, the epoch summary, the thinking panel, the blank-thread
 * state, and group chat. Passing it down would mean threading a prop through
 * every one; each keeping its own copy is how they drifted apart before. It is
 * a custom property on the document element instead, set once at boot next to
 * the theme, which is the same shape of preference.
 */

export type ChatWidth = "comfortable" | "wide";

export const CHAT_WIDTH_STORAGE_KEY = "stella-chat-width";

export const DEFAULT_CHAT_WIDTH: ChatWidth = "comfortable";

/** Matches `max-w-3xl`, the width every one of these components used before. */
const COMFORTABLE = "48rem";

/** `max-w-6xl`. Past this a line of Latin body copy stops being scannable, and
 *  the payload blocks that motivate wide mode rarely need more. */
const WIDE = "72rem";

export function getStoredChatWidth(): ChatWidth {
  const browserWindow = globalThis.window;
  if (!browserWindow) return DEFAULT_CHAT_WIDTH;
  const stored = browserWindow.localStorage.getItem(CHAT_WIDTH_STORAGE_KEY);
  return stored === "wide" || stored === "comfortable" ? stored : DEFAULT_CHAT_WIDTH;
}

export function applyChatWidth(width: ChatWidth) {
  document.documentElement.style.setProperty(
    "--chat-column",
    width === "wide" ? WIDE : COMFORTABLE,
  );
}

export function setStoredChatWidth(width: ChatWidth) {
  window.localStorage.setItem(CHAT_WIDTH_STORAGE_KEY, width);
  applyChatWidth(width);
}
