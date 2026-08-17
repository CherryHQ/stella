import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { ArrowUp, Paperclip, Plus, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { useI18n } from "@/lib/i18n";
import {
  applyTriggerSelection,
  filterTriggerItems,
  findTriggerFragment,
  type ComposerSkill,
  type ComposerTrigger,
  type ComposerTriggerItem,
  type TriggerFragment,
} from "./composer-triggers";
import { loadDraft, saveDraft, type ComposerDraft } from "./draft-store";

export interface Attachment {
  name: string;
  path: string;
  uploading: boolean;
  /** Browser-reported MIME type. Advisory: the server re-detects from bytes. */
  mediaType?: string;
}

export const BUILTIN_COMMANDS: ComposerSkill[] = [
  { name: "compact", description: "Compact session memory" },
];

interface Props {
  onSend: (text: string) => void;
  onStop?: () => void;
  isStreaming: boolean;
  disabled?: boolean;
  placeholder?: string;
  attachments?: Attachment[];
  onFileSelect?: (files: FileList) => void;
  onRemoveAttachment?: (idx: number) => void;
  /** Autocomplete menus keyed by their trigger char; first match near the caret wins. */
  triggers?: ComposerTrigger[];
  /**
   * Identifies this conversation's draft. When set, the text, its pinned chips
   * and the last sent message survive reloads and thread switches (see
   * draft-store). Without it the composer starts empty every mount.
   */
  draftKey?: string;
}

export function ChatComposer({
  onSend,
  onStop,
  isStreaming,
  disabled,
  placeholder = "Send a message…",
  attachments,
  onFileSelect,
  onRemoveAttachment,
  triggers,
  draftKey,
}: Props) {
  const { t } = useI18n();
  const fileInputRef = useRef<HTMLInputElement>(null);
  const taRef = useRef<HTMLTextAreaElement | null>(null);
  // Caret position to restore after code rewrites the draft. React commits the
  // new value first, so the move must happen in a layout effect.
  const pendingCaretRef = useRef<number | null>(null);

  const draftRef = useRef<ComposerDraft>(loadDraft(draftKey ?? null));
  const [chips, setChips] = useState<ComposerTriggerItem[]>(draftRef.current.chips);
  const [value, setValueState] = useState(draftRef.current.text);

  useEffect(() => {
    const restored = loadDraft(draftKey ?? null);
    draftRef.current = restored;
    setValueState(restored.text);
    setChips(restored.chips);
  }, [draftKey]);

  const persist = useCallback(
    (patch: Partial<ComposerDraft>) => {
      draftRef.current = { ...draftRef.current, ...patch };
      saveDraft(draftKey ?? null, draftRef.current);
    },
    [draftKey],
  );

  const resizeTextarea = useCallback(() => {
    const textarea = taRef.current;
    if (!textarea) return;
    textarea.style.height = "auto";
    textarea.style.height = `${Math.min(textarea.scrollHeight, 160)}px`;
  }, []);

  useEffect(() => {
    resizeTextarea();
  }, [value, resizeTextarea]);

  const setValue = useCallback(
    (v: string) => {
      setValueState(v);
      persist({ text: v });
    },
    [persist],
  );

  // Not an updater form on purpose: persisting inside a state updater would
  // write twice under StrictMode's double invocation.
  const setChipsPersisted = useCallback(
    (next: ComposerTriggerItem[]) => {
      setChips(next);
      persist({ chips: next });
    },
    [persist],
  );

  const hasAttachments = attachments && attachments.length > 0;
  const canSend =
    !isStreaming &&
    !disabled &&
    (value.trim() ||
      chips.length > 0 ||
      (hasAttachments && attachments.some((a) => !a.uploading))) &&
    !attachments?.some((a) => a.uploading);

  const handleSend = useCallback(() => {
    if (!canSend) return;
    let full = value;
    if (chips.length > 0) {
      const prefix = chips.map((c) => c.label).join(" ");
      full = value.trim() ? `${prefix} ${value}` : prefix;
    }
    setChips([]);
    setValueState("");
    // Keep the sent text so an empty composer can recall it with ArrowUp.
    persist({ text: "", chips: [], lastSent: full });
    onSend(full);
  }, [canSend, chips, value, persist, onSend]);

  /** Recall the last sent message when ArrowUp is pressed in an empty composer. */
  const recallLastSent = useCallback(() => {
    const last = draftRef.current.lastSent;
    if (!last) return false;
    setValue(last);
    pendingCaretRef.current = last.length;
    return true;
  }, [setValue]);

  const [menu, setMenu] = useState<TriggerFragment | null>(null);
  const [menuIndex, setMenuIndex] = useState(0);
  const menuListRef = useRef<HTMLDivElement>(null);
  const [dragging, setDragging] = useState(false);
  const dragDepthRef = useRef(0);

  const detectTrigger = useCallback(
    (val: string, caret: number) => {
      setMenu(triggers?.length ? findTriggerFragment(val, caret, triggers) : null);
    },
    [triggers],
  );

  const handleChange = useCallback(
    (val: string) => {
      setValue(val);
      detectTrigger(val, taRef.current?.selectionStart ?? val.length);
    },
    [setValue, detectTrigger],
  );

  const activeTrigger = useMemo(
    () => (menu ? triggers?.find((tr) => tr.char === menu.char) : undefined),
    [menu, triggers],
  );

  const candidates = useMemo(() => {
    if (!menu || !activeTrigger) return [];
    return filterTriggerItems(activeTrigger, menu.query, new Set(chips.map((c) => c.key)));
  }, [menu, activeTrigger, chips]);

  useEffect(() => {
    setMenuIndex(0);
  }, [candidates.length]);

  const menuOpen = menu !== null && candidates.length > 0;

  useLayoutEffect(() => {
    const caret = pendingCaretRef.current;
    if (caret === null) return;
    pendingCaretRef.current = null;
    taRef.current?.setSelectionRange(caret, caret);
  }, [value]);

  const selectItem = useCallback(
    (item: ComposerTriggerItem) => {
      const textarea = taRef.current;
      if (!textarea || !activeTrigger || !menu) return;

      const next = applyTriggerSelection(
        value,
        textarea.selectionStart ?? value.length,
        menu,
        activeTrigger.replace(item),
      );
      setValue(next.value);
      pendingCaretRef.current = next.caret;
      if (activeTrigger.chip) setChipsPersisted([...chips, item]);
      setMenu(null);
      textarea.focus();
    },
    [value, setValue, activeTrigger, menu, chips, setChipsPersisted],
  );

  const removeChip = useCallback(
    (key: string) => {
      setChipsPersisted(chips.filter((c) => c.key !== key));
    },
    [chips, setChipsPersisted],
  );

  const moveMenu = useCallback(
    (delta: number) => {
      const next = (menuIndex + delta + candidates.length) % candidates.length;
      setMenuIndex(next);
      menuListRef.current
        ?.querySelector(`[data-index="${next}"]`)
        ?.scrollIntoView({ block: "nearest" });
    },
    [menuIndex, candidates.length],
  );

  const menuOverlay = menuOpen ? (
    <div
      ref={menuListRef}
      className="absolute bottom-full left-0 right-0 z-10 mb-1 max-h-48 overflow-y-auto rounded-lg border border-border bg-popover"
    >
      <div className="p-1.5">
        {candidates.map((item, i) => (
          <button
            key={item.key}
            type="button"
            data-index={i}
            onClick={() => selectItem(item)}
            onMouseEnter={() => setMenuIndex(i)}
            className={cn(
              "group flex w-full items-baseline gap-3 rounded-md px-2.5 py-2 text-left transition-colors",
              i === menuIndex ? "bg-muted/50" : "",
            )}
          >
            <code
              className={cn(
                "shrink-0 rounded px-1.5 py-0.5 font-mono text-xs font-semibold",
                i === menuIndex ? "bg-muted text-foreground" : "bg-muted text-muted-foreground",
              )}
            >
              {item.label}
            </code>
            {item.description && (
              <span className="truncate text-xs leading-tight text-muted-foreground">
                {item.description}
              </span>
            )}
          </button>
        ))}
      </div>
    </div>
  ) : null;

  const hasChips = hasAttachments || chips.length > 0;
  const hasSkillTrigger = triggers?.some((tr) => tr.char === "/") ?? false;

  return (
    <div className="relative min-w-0 flex-shrink-0 px-4 pt-2 pb-3 sm:px-8">
      {onFileSelect && (
        <input
          ref={fileInputRef}
          type="file"
          multiple
          className="hidden"
          onChange={(e) => {
            if (e.target.files) onFileSelect(e.target.files);
            e.target.value = "";
          }}
        />
      )}
      <div
        className={cn(
          "relative mx-auto flex w-full min-w-0 max-w-[var(--chat-column)] flex-col rounded-xl border bg-card p-2",
          isStreaming
            ? "border-primary focus-within:ring-2 focus-within:ring-primary/20"
            : "border-border focus-within:border-primary focus-within:ring-2 focus-within:ring-primary/20",
          dragging && onFileSelect ? "border-primary ring-2 ring-primary/20" : "",
        )}
        onDragEnter={(e) => {
          if (!onFileSelect || isStreaming) return;
          e.preventDefault();
          e.stopPropagation();
          dragDepthRef.current += 1;
          setDragging(true);
        }}
        onDragOver={(e) => {
          if (!onFileSelect || isStreaming) return;
          e.preventDefault();
          e.stopPropagation();
        }}
        onDragLeave={() => {
          if (!onFileSelect) return;
          dragDepthRef.current = Math.max(0, dragDepthRef.current - 1);
          if (dragDepthRef.current === 0) setDragging(false);
        }}
        onDrop={(e) => {
          if (!onFileSelect) return;
          e.preventDefault();
          e.stopPropagation();
          dragDepthRef.current = 0;
          setDragging(false);
          if (isStreaming) return;
          onFileSelect(e.dataTransfer.files);
        }}
      >
        {menuOverlay}
        <div className="relative min-w-0">
          <textarea
            ref={taRef}
            value={value}
            onChange={(e) => handleChange(e.target.value)}
            onKeyDown={(e) => {
              // IME (e.g. pinyin) Enter confirms composition; React's synthetic
              // event hides isComposing, so check the native event first.
              if (e.nativeEvent.isComposing || e.keyCode === 229) return;
              if (menuOpen) {
                if (e.key === "ArrowDown") {
                  e.preventDefault();
                  moveMenu(1);
                  return;
                }
                if (e.key === "ArrowUp") {
                  e.preventDefault();
                  moveMenu(-1);
                  return;
                }
                if (e.key === "Enter" || e.key === "Tab") {
                  e.preventDefault();
                  selectItem(candidates[menuIndex]);
                  return;
                }
                if (e.key === "Escape") {
                  e.preventDefault();
                  setMenu(null);
                  return;
                }
              }
              // An empty composer treats ArrowUp as "bring back what I just
              // sent", the shell habit; with text in it, ArrowUp still moves
              // the caret.
              if (e.key === "ArrowUp" && !value && recallLastSent()) {
                e.preventDefault();
                return;
              }
              // Escape stops the turn so the keyboard alone can interrupt a
              // long answer without reaching for the stop button.
              if (e.key === "Escape" && isStreaming && onStop) {
                e.preventDefault();
                onStop();
                return;
              }
              if (e.key === "Enter" && !e.shiftKey) {
                // Typing during a turn is allowed; sending is not, so swallow
                // Enter instead of losing the draft to a no-op.
                e.preventDefault();
                handleSend();
              }
            }}
            onClick={() => detectTrigger(value, taRef.current?.selectionStart ?? value.length)}
            onPaste={(e) => {
              if (!onFileSelect || isStreaming) return;
              const files = e.clipboardData.files;
              if (files.length > 0) {
                e.preventDefault();
                onFileSelect(files);
              }
            }}
            placeholder={placeholder}
            className="max-h-40 min-h-10 w-full min-w-0 resize-none overflow-y-auto border-0 bg-transparent px-4 py-2.5 text-sm leading-relaxed text-foreground placeholder:text-muted-foreground focus:outline-none"
            rows={1}
            disabled={disabled}
          />
        </div>
        {hasChips && (
          <div className="flex flex-wrap gap-1.5 px-3 pb-2">
            {chips.map((c) => (
              <span
                key={c.key}
                className="inline-flex items-center gap-1 rounded-md border border-border bg-muted px-2.5 py-1 font-mono text-xs font-semibold text-foreground"
              >
                {c.label}
                <button
                  type="button"
                  onClick={() => removeChip(c.key)}
                  className="ml-0.5 shrink-0 cursor-pointer text-muted-foreground transition-colors hover:text-foreground"
                >
                  <X className="size-3" />
                </button>
              </span>
            ))}
            {hasAttachments &&
              attachments.map((a, i) => (
                <span
                  key={i}
                  className={cn(
                    "inline-flex max-w-48 items-center gap-1.5 rounded-md border px-2.5 py-1 font-mono text-xs",
                    a.uploading
                      ? "border-border bg-muted/50 text-muted-foreground"
                      : "border-border bg-muted text-muted-foreground",
                  )}
                >
                  {a.uploading ? (
                    <div className="size-3 shrink-0 animate-spin rounded-full border border-muted-foreground/30 border-t-muted-foreground" />
                  ) : (
                    <Paperclip className="size-3 shrink-0 text-muted-foreground" />
                  )}
                  <span className="truncate">{a.name}</span>
                  {!a.uploading && onRemoveAttachment && (
                    <button
                      type="button"
                      onClick={() => onRemoveAttachment(i)}
                      className="ml-0.5 shrink-0 cursor-pointer text-muted-foreground transition-colors hover:text-foreground"
                    >
                      <X className="size-3" />
                    </button>
                  )}
                </span>
              ))}
          </div>
        )}
        <div className="flex min-w-0 items-center gap-1.5 px-2 pb-0.5">
          {!isStreaming && onFileSelect && (
            <Button
              variant="ghost"
              size="icon-xs"
              onClick={() => fileInputRef.current?.click()}
              title={t("sessions.composer.attachFiles")}
            >
              <Plus className="size-4" />
            </Button>
          )}
          {!isStreaming && (
            <span className="min-w-0 truncate font-mono text-xs text-muted-foreground select-none">
              {hasSkillTrigger
                ? t("sessions.transcript.sendHintSkills")
                : t("sessions.transcript.sendHint")}
            </span>
          )}
          {isStreaming && (
            <>
              <span className="text-xs font-mono text-info select-none animate-pulse">
                {t("sessions.transcript.generating")}
              </span>
              {onStop && (
                <span className="min-w-0 truncate font-mono text-xs text-muted-foreground select-none">
                  {t("sessions.transcript.streamingHint")}
                </span>
              )}
            </>
          )}
          <div className="ml-auto shrink-0">
            {isStreaming && onStop ? (
              <Button variant="destructive-outline" size="sm" onClick={onStop}>
                <div className="w-2 h-2 bg-destructive rounded-xs" />
                <span>{t("sessions.composer.stop")}</span>
              </Button>
            ) : (
              <Button
                size="icon-sm"
                variant={canSend ? "default" : "ghost"}
                disabled={!canSend}
                onClick={handleSend}
                title={t("sessions.composer.sendMessage")}
              >
                <ArrowUp className="size-4 stroke-[2.5]" />
              </Button>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}
