/**
 * Composer autocomplete triggers. "/" skills and "@" mentions are one
 * mechanism: match a `<char><query>` fragment at the caret, then rewrite that
 * fragment when a row is picked. Keeping the text math here (rather than in
 * ChatComposer) makes it testable without a DOM.
 */

export interface ComposerSkill {
  name: string;
  description: string;
}

/** One row of a trigger menu. `label` carries the char, e.g. "/compact", "@bob". */
export interface ComposerTriggerItem {
  key: string;
  label: string;
  description?: string;
}

export interface ComposerTrigger {
  char: string;
  items: ComposerTriggerItem[];
  /**
   * Text that replaces the `<char><query>` fragment on select. Return "" to
   * drop the fragment entirely, which only makes sense with `chip`.
   */
  replace: (item: ComposerTriggerItem) => string;
  /**
   * Pin the selection as a removable chip above the send row instead of
   * leaving it in the text; chip labels are prefixed to the sent message.
   */
  chip?: boolean;
}

export interface TriggerFragment {
  char: string;
  query: string;
  /** Index of the trigger char in the full value. */
  at: number;
}

/** Skills as a "/" trigger: picking one pins a chip and clears the fragment. */
export function skillTrigger(skills: ComposerSkill[]): ComposerTrigger {
  return {
    char: "/",
    items: skills.map((s) => ({ key: s.name, label: `/${s.name}`, description: s.description })),
    replace: () => "",
    chip: true,
  };
}

/**
 * The trigger fragment the caret sits in, or null. The char must start a word,
 * so "a/b" and "user@host" never open a menu; with several triggers in range
 * the one closest to the caret wins.
 */
export function findTriggerFragment(
  value: string,
  caret: number,
  triggers: ComposerTrigger[],
): TriggerFragment | null {
  const before = value.slice(0, Math.max(0, Math.min(caret, value.length)));
  let best: TriggerFragment | null = null;
  for (const trigger of triggers) {
    const escaped = trigger.char.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    const match = before.match(new RegExp(`(?:^|\\s)${escaped}(\\S*)$`));
    if (!match) continue;
    const at = before.length - match[1].length - trigger.char.length;
    if (!best || at > best.at) best = { char: trigger.char, query: match[1], at };
  }
  return best;
}

/**
 * Items matching the fragment query, minus the ones already pinned as chips,
 * best match first. Descriptions are searched too, but they rank last: typing
 * "/comp" must not bury /compact under every skill whose prose says "compose".
 */
export function filterTriggerItems(
  trigger: ComposerTrigger,
  query: string,
  pinnedKeys: ReadonlySet<string>,
): ComposerTriggerItem[] {
  const q = query.toLowerCase();
  return trigger.items
    .filter((item) => !(trigger.chip && pinnedKeys.has(item.key)))
    .map((item, index) => ({ item, index, rank: matchRank(trigger, item, q) }))
    .filter((entry) => entry.rank < RANK_NONE)
    .sort((a, b) => a.rank - b.rank || a.index - b.index)
    .map((entry) => entry.item);
}

const RANK_NONE = 3;

function matchRank(trigger: ComposerTrigger, item: ComposerTriggerItem, query: string): number {
  const name = (
    item.label.startsWith(trigger.char) ? item.label.slice(trigger.char.length) : item.label
  ).toLowerCase();
  if (name.startsWith(query)) return 0;
  if (name.includes(query)) return 1;
  if (item.description?.toLowerCase().includes(query)) return 2;
  return RANK_NONE;
}

/**
 * Replace the fragment with `replacement`, returning the new draft and where
 * the caret belongs (right after the inserted text, not at the end).
 */
export function applyTriggerSelection(
  value: string,
  caret: number,
  fragment: TriggerFragment,
  replacement: string,
) {
  const end = Math.max(0, Math.min(caret, value.length));
  return {
    value: value.slice(0, fragment.at) + replacement + value.slice(end),
    caret: fragment.at + replacement.length,
  };
}
