import type { ComposerTriggerItem } from "./composer-triggers";

/**
 * Per-conversation composer drafts, kept in sessionStorage so a reload or a
 * detour to another thread does not lose half-typed input. Storage is
 * best-effort: a full or blocked sessionStorage degrades to "no draft", never
 * to a broken composer.
 */

const PREFIX = "stella-draft:";

/** Drafts are cheap but unbounded otherwise: deleted threads never clean up. */
const MAX_DRAFTS = 20;

/** An attachment that finished uploading, so it can be restored by path. */
export interface DraftAttachment {
  name: string;
  path: string;
  mediaType?: string;
}

export interface ComposerDraft {
  text: string;
  /** Pinned trigger selections, e.g. "/compact", restored alongside the text. */
  chips: ComposerTriggerItem[];
  /** Uploaded attachments; in-flight and failed ones are deliberately not kept. */
  attachments: DraftAttachment[];
  /** Last message sent from this composer, for recall on ArrowUp. */
  lastSent?: string;
}

interface StoredDraft extends ComposerDraft {
  updatedAt: number;
}

const EMPTY: ComposerDraft = { text: "", chips: [], attachments: [] };

function isStoredString(value: StoredDraft["text"] | StoredDraft["lastSent"]): value is string {
  return typeof value === "string";
}

function storage(): Storage | null {
  try {
    return globalThis.sessionStorage ?? null;
  } catch {
    return null;
  }
}

export function loadDraft(key: string | null): ComposerDraft {
  const store = key && storage();
  if (!store || !key) return EMPTY;
  const raw = store.getItem(PREFIX + key);
  if (!raw) return EMPTY;
  try {
    // SAFETY: the persisted draft is stored as JSON of the StoredDraft shape.
    const parsed = JSON.parse(raw) as StoredDraft;
    if (!isStoredString(parsed?.text)) return EMPTY;
    return {
      text: parsed.text,
      chips: Array.isArray(parsed.chips) ? parsed.chips : [],
      attachments: Array.isArray(parsed.attachments) ? parsed.attachments : [],
      lastSent: isStoredString(parsed.lastSent) ? parsed.lastSent : undefined,
    };
  } catch {
    // Drafts written before this format were bare strings.
    return { ...EMPTY, text: raw };
  }
}

/**
 * Merge a partial draft into what is stored. The composer owns the text and
 * chips while the attachment hook owns the files, so neither may write the
 * whole record: a blind overwrite would drop the other one's fields.
 */
export function patchDraft(key: string | null, patch: Partial<ComposerDraft>): void {
  if (!key) return;
  saveDraft(key, { ...loadDraft(key), ...patch });
}

function saveDraft(key: string | null, draft: ComposerDraft): void {
  const store = key && storage();
  if (!store || !key) return;
  const storageKey = PREFIX + key;
  if (
    !draft.text &&
    draft.chips.length === 0 &&
    draft.attachments.length === 0 &&
    !draft.lastSent
  ) {
    store.removeItem(storageKey);
    return;
  }
  const isNew = store.getItem(storageKey) === null;
  const stored: StoredDraft = { ...draft, updatedAt: Date.now() };
  try {
    store.setItem(storageKey, JSON.stringify(stored));
  } catch {
    return; // Quota or private mode: the draft simply is not persisted.
  }
  if (isNew) pruneDrafts(store);
}

/** Drop the least recently updated drafts once past MAX_DRAFTS. */
function pruneDrafts(store: Storage): void {
  const entries: { key: string; updatedAt: number }[] = [];
  for (let i = 0; i < store.length; i++) {
    const key = store.key(i);
    if (!key?.startsWith(PREFIX)) continue;
    let updatedAt = 0;
    try {
      // SAFETY: the persisted draft is JSON of the StoredDraft shape; updatedAt is opt-in.
      updatedAt = (JSON.parse(store.getItem(key) ?? "{}") as StoredDraft).updatedAt ?? 0;
    } catch {
      // Legacy string draft: no timestamp, so evict it first.
    }
    entries.push({ key, updatedAt });
  }
  if (entries.length <= MAX_DRAFTS) return;
  entries.sort((a, b) => a.updatedAt - b.updatedAt);
  for (const entry of entries.slice(0, entries.length - MAX_DRAFTS)) {
    store.removeItem(entry.key);
  }
}
