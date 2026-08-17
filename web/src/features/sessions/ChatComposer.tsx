import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { ArrowUp, Paperclip, Plus, TriangleAlert, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { formatBytes } from "@/lib/format-bytes";
import { useMediaQuery } from "@/hooks/use-media-query";
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
  size?: number;
  /** Object URL for a local image preview; revoked when the chip goes away. */
  previewUrl?: string;
  /** Set when the upload failed. The chip stays so the user can drop it explicitly. */
  error?: string;
}

const MENU_ID = "composer-trigger-menu";
const optionId = (index: number) => `${MENU_ID}-option-${index}`;

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
  // On a touch keyboard Enter is the only way to get a new line, so it must not
  // send; the button is the send affordance there.
  const isTouch = useMediaQuery({ pointer: "coarse" });
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
    // Read the cap from CSS so the responsive max-height stays single-sourced.
    const max = Number.parseFloat(getComputedStyle(textarea).maxHeight);
    textarea.style.height = `${Math.min(textarea.scrollHeight, Number.isFinite(max) ? max : 160)}px`;
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
    (value.trim() || chips.length > 0 || (hasAttachments && attachments.some((a) => a.path))) &&
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
  // Dragging is tracked on the document so a file dropped anywhere in the app
  // attaches, instead of bouncing off the page and opening in a new tab.
  const [focused, setFocused] = useState(false);
  const [dragging, setDragging] = useState(false);
  const dragDepthRef = useRef(0);
  const canAttach = !!onFileSelect && !isStreaming;

  useEffect(() => {
    if (!canAttach) {
      setDragging(false);
      dragDepthRef.current = 0;
      return;
    }
    const carriesFiles = (e: DragEvent) => e.dataTransfer?.types.includes("Files") ?? false;
    const onEnter = (e: DragEvent) => {
      if (!carriesFiles(e)) return;
      e.preventDefault();
      dragDepthRef.current += 1;
      setDragging(true);
    };
    const onOver = (e: DragEvent) => {
      if (carriesFiles(e)) e.preventDefault();
    };
    const onLeave = () => {
      dragDepthRef.current = Math.max(0, dragDepthRef.current - 1);
      if (dragDepthRef.current === 0) setDragging(false);
    };
    const onDrop = (e: DragEvent) => {
      if (!carriesFiles(e)) return;
      e.preventDefault();
      dragDepthRef.current = 0;
      setDragging(false);
      if (e.dataTransfer?.files.length) onFileSelect?.(e.dataTransfer.files);
    };
    document.addEventListener("dragenter", onEnter);
    document.addEventListener("dragover", onOver);
    document.addEventListener("dragleave", onLeave);
    document.addEventListener("drop", onDrop);
    return () => {
      document.removeEventListener("dragenter", onEnter);
      document.removeEventListener("dragover", onOver);
      document.removeEventListener("dragleave", onLeave);
      document.removeEventListener("drop", onDrop);
    };
  }, [canAttach, onFileSelect]);

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
      id={MENU_ID}
      role="listbox"
      aria-label={t("sessions.composer.suggestions")}
      className="absolute bottom-full left-0 right-0 z-10 mb-1 max-h-48 overflow-y-auto rounded-lg border border-border bg-popover"
    >
      <div className="p-1.5">
        {candidates.map((item, i) => (
          <button
            key={item.key}
            type="button"
            role="option"
            id={optionId(i)}
            aria-selected={i === menuIndex}
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
      {dragging && (
        <div
          aria-hidden
          className="pointer-events-none fixed inset-0 z-50 flex items-center justify-center bg-background/70 backdrop-blur-xs"
        >
          <div className="flex items-center gap-2 rounded-xl border-2 border-dashed border-primary bg-card px-6 py-4 text-sm text-foreground">
            <Paperclip className="size-4 text-muted-foreground" />
            {t("sessions.composer.dropHint")}
          </div>
        </div>
      )}
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
          dragging ? "border-primary ring-2 ring-primary/20" : "",
        )}
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
              if (e.key === "Enter" && !e.shiftKey && !isTouch) {
                // Typing during a turn is allowed; sending is not, so swallow
                // Enter instead of losing the draft to a no-op.
                e.preventDefault();
                handleSend();
              }
            }}
            onClick={() => detectTrigger(value, taRef.current?.selectionStart ?? value.length)}
            onFocus={() => setFocused(true)}
            onBlur={() => setFocused(false)}
            onPaste={(e) => {
              if (!onFileSelect || isStreaming) return;
              const files = e.clipboardData.files;
              if (files.length > 0) {
                e.preventDefault();
                onFileSelect(files);
              }
            }}
            placeholder={placeholder}
            aria-label={placeholder}
            role="combobox"
            aria-expanded={menuOpen}
            aria-controls={menuOpen ? MENU_ID : undefined}
            aria-activedescendant={menuOpen ? optionId(menuIndex) : undefined}
            aria-autocomplete="list"
            className="max-h-[40vh] min-h-10 w-full sm:max-h-40 min-w-0 resize-none overflow-y-auto border-0 bg-transparent px-4 py-2.5 text-sm leading-relaxed text-foreground placeholder:text-muted-foreground focus:outline-none"
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
                  aria-label={t("sessions.composer.removeItem", { item: c.label })}
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
                  title={a.error ?? (a.size ? `${a.name} · ${formatBytes(a.size)}` : a.name)}
                  className={cn(
                    "inline-flex max-w-48 items-center gap-1.5 rounded-md border py-1 pr-2.5 font-mono text-xs",
                    a.previewUrl ? "pl-1" : "pl-2.5",
                    a.error
                      ? "border-destructive/40 bg-destructive/10 text-destructive-foreground"
                      : a.uploading
                        ? "border-border bg-muted/50 text-muted-foreground"
                        : "border-border bg-muted text-muted-foreground",
                  )}
                >
                  {a.previewUrl ? (
                    <img
                      src={a.previewUrl}
                      alt=""
                      className={cn(
                        "size-6 shrink-0 rounded object-cover",
                        a.uploading ? "opacity-50" : "",
                      )}
                    />
                  ) : a.uploading ? (
                    <div className="size-3 shrink-0 animate-spin rounded-full border border-muted-foreground/30 border-t-muted-foreground" />
                  ) : a.error ? (
                    <TriangleAlert className="size-3 shrink-0" />
                  ) : (
                    <Paperclip className="size-3 shrink-0 text-muted-foreground" />
                  )}
                  <span className="truncate">{a.name}</span>
                  {!a.uploading && onRemoveAttachment && (
                    <button
                      type="button"
                      onClick={() => onRemoveAttachment(i)}
                      aria-label={t("sessions.composer.removeItem", { item: a.name })}
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
              aria-label={t("sessions.composer.attachFiles")}
            >
              <Plus className="size-4" />
            </Button>
          )}
          {!isStreaming && !isTouch && focused && !value && (
            <span className="min-w-0 truncate font-mono text-xs text-muted-foreground select-none">
              {hasSkillTrigger
                ? t("sessions.transcript.sendHintSkills")
                : t("sessions.transcript.sendHint")}
            </span>
          )}
          {isStreaming && (
            <span role="status" className="text-xs font-mono text-info select-none animate-pulse">
              {t("sessions.transcript.generating")}
            </span>
          )}
          <div className="ml-auto shrink-0">
            {isStreaming && onStop ? (
              <Button
                variant="destructive-outline"
                size="sm"
                onClick={onStop}
                title={t("sessions.composer.stopHint")}
              >
                <div className="w-2 h-2 bg-destructive rounded-xs" />
                <span>{t("sessions.composer.stop")}</span>
              </Button>
            ) : (
              <Button
                size="icon-sm"
                variant={canSend ? "default" : "ghost"}
                disabled={!canSend}
                onClick={handleSend}
                title={t("sessions.composer.sendHint")}
                aria-label={t("sessions.composer.sendMessage")}
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
