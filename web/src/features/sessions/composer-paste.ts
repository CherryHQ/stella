/**
 * Pasting a wall of text (a log dump, a whole file) into the composer turns it
 * into a 40vh scrollbar and ships the whole thing as prose. Past a threshold it
 * becomes a text attachment instead.
 */

/**
 * Chars above which a paste becomes a file. Deliberately generous: a pasted
 * function or stack trace should stay editable text, only dumps get filed.
 * Lower it if users report the composer choking on medium pastes.
 */
export const PASTE_AS_FILE_CHARS = 4000;

export function shouldPasteAsFile(text: string): boolean {
  return text.length > PASTE_AS_FILE_CHARS;
}

/** Sortable, collision-free within a second, and obvious in a file list. */
export function pastedFileName(now: Date = new Date()): string {
  const stamp = now
    .toISOString()
    .replace(/[-:]/g, "")
    .replace(/\.\d+Z$/, "");
  return `pasted-${stamp}.txt`;
}
