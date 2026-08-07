import type { ReactNode, RefObject } from "react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ArrowUp, Paperclip, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { useI18n } from "@/lib/i18n";

export interface Attachment {
  name: string;
  path: string;
  uploading: boolean;
  /** Browser-reported MIME type. Advisory: the server re-detects from bytes. */
  mediaType?: string;
}

export interface ComposerSkill {
  name: string;
  description: string;
}

export const BUILTIN_COMMANDS: ComposerSkill[] = [
  { name: "compact", description: "Compact session memory" },
];

interface Props {
  /**
   * Controlled mode: pass value + onChange when the parent must observe or
   * rewrite the draft (e.g. GroupChat's @-mention insertion). Omit both for
   * uncontrolled mode, where the draft lives here — keystrokes then re-render
   * only the composer, not the parent page and its transcript.
   */
  value?: string;
  onChange?: (value: string) => void;
  onSend: (text: string) => void;
  onStop?: () => void;
  isStreaming: boolean;
  disabled?: boolean;
  placeholder?: string;
  attachments?: Attachment[];
  onFileSelect?: (files: FileList) => void;
  onRemoveAttachment?: (idx: number) => void;
  overlay?: ReactNode;
  textareaRef?: RefObject<HTMLTextAreaElement | null>;
  skills?: ComposerSkill[];
}

export function ChatComposer({
  value: valueProp,
  onChange,
  onSend,
  onStop,
  isStreaming,
  disabled,
  placeholder = "Send a message…",
  attachments,
  onFileSelect,
  onRemoveAttachment,
  overlay,
  textareaRef,
  skills,
}: Props) {
  const { t } = useI18n();
  const fileInputRef = useRef<HTMLInputElement>(null);
  const internalTextareaRef = useRef<HTMLTextAreaElement | null>(null);
  const taRef = textareaRef ?? internalTextareaRef;

  const [selectedSkills, setSelectedSkills] = useState<ComposerSkill[]>([]);

  const [draft, setDraft] = useState("");
  const value = valueProp ?? draft;
  const setValue = useCallback(
    (v: string) => {
      if (valueProp === undefined) setDraft(v);
      onChange?.(v);
    },
    [valueProp, onChange],
  );

  const hasAttachments = attachments && attachments.length > 0;
  const canSend =
    (value.trim() ||
      selectedSkills.length > 0 ||
      (hasAttachments && attachments.some((a) => !a.uploading))) &&
    !attachments?.some((a) => a.uploading);

  const handleSend = useCallback(() => {
    if (isStreaming || disabled || !canSend) return;
    let full = value;
    if (selectedSkills.length > 0) {
      const prefix = selectedSkills.map((s) => `/${s.name}`).join(" ");
      full = value.trim() ? `${prefix} ${value}` : prefix;
      setSelectedSkills([]);
    }
    setValue("");
    onSend(full);
  }, [isStreaming, disabled, canSend, selectedSkills, value, setValue, onSend]);

  const [slashQuery, setSlashQuery] = useState<string | null>(null);
  const [slashIndex, setSlashIndex] = useState(0);
  const slashListRef = useRef<HTMLDivElement>(null);

  const detectSlash = useCallback(
    (val: string) => {
      if (!skills || skills.length === 0) {
        setSlashQuery(null);
        return;
      }
      const textarea = taRef.current;
      const pos = textarea?.selectionStart ?? val.length;
      const before = val.slice(0, pos);
      const match = before.match(/(?:^|\s)\/(\S*)$/);
      setSlashQuery(match ? match[1] : null);
    },
    [skills, taRef],
  );

  const handleChange = useCallback(
    (val: string) => {
      setValue(val);
      detectSlash(val);
    },
    [setValue, detectSlash],
  );

  const slashCandidates = useMemo(() => {
    if (slashQuery === null || !skills) return [];
    const q = slashQuery.toLowerCase();
    const alreadySelected = new Set(selectedSkills.map((s) => s.name));
    return skills.filter(
      (s) =>
        !alreadySelected.has(s.name) &&
        (s.name.toLowerCase().includes(q) || s.description.toLowerCase().includes(q)),
    );
  }, [slashQuery, skills, selectedSkills]);

  useEffect(() => {
    setSlashIndex(0);
  }, [slashCandidates.length]);

  const slashOpen = slashQuery !== null && slashCandidates.length > 0;

  const insertSkill = useCallback(
    (skill: ComposerSkill) => {
      const textarea = taRef.current;
      if (!textarea) return;

      const pos = textarea.selectionStart ?? value.length;
      const before = value.slice(0, pos);
      const after = value.slice(pos);
      const slashIdx = before.lastIndexOf("/");
      if (slashIdx < 0) return;

      const newVal = (before.slice(0, slashIdx) + after).trim();
      setValue(newVal);
      setSelectedSkills((prev) => [...prev, skill]);
      setSlashQuery(null);
      textarea.focus();
    },
    [value, setValue, taRef],
  );

  const removeSkill = useCallback((name: string) => {
    setSelectedSkills((prev) => prev.filter((s) => s.name !== name));
  }, []);

  const slashOverlay = slashOpen ? (
    <div
      ref={slashListRef}
      className="absolute bottom-full left-0 right-0 z-10 mb-1 max-h-48 overflow-y-auto rounded-lg border border-border bg-popover"
    >
      <div className="p-1.5">
        {slashCandidates.map((s, i) => (
          <button
            key={s.name}
            type="button"
            data-index={i}
            onClick={() => insertSkill(s)}
            onMouseEnter={() => setSlashIndex(i)}
            className={cn(
              "group flex w-full items-baseline gap-3 rounded-md px-2.5 py-2 text-left transition-colors",
              i === slashIndex ? "bg-muted/50" : "",
            )}
          >
            <code
              className={cn(
                "shrink-0 rounded px-1.5 py-0.5 font-mono text-xs font-semibold",
                i === slashIndex ? "bg-muted text-foreground" : "bg-muted text-muted-foreground",
              )}
            >
              /{s.name}
            </code>
            <span className="truncate text-xs leading-tight text-muted-foreground">
              {s.description}
            </span>
          </button>
        ))}
      </div>
    </div>
  ) : null;

  const hasChips = (hasAttachments && attachments.length > 0) || selectedSkills.length > 0;

  return (
    <div className="relative min-w-0 flex-shrink-0 px-4 pt-2 pb-3 sm:px-8">
      {overlay}
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
          "relative mx-auto flex w-full min-w-0 max-w-[var(--chat-column)] flex-col rounded-lg border bg-card p-1.5",
          isStreaming
            ? "border-primary focus-within:ring-2 focus-within:ring-primary/20"
            : "border-border focus-within:border-primary focus-within:ring-2 focus-within:ring-primary/20",
        )}
        onDragOver={(e) => {
          if (!onFileSelect) return;
          e.preventDefault();
          e.stopPropagation();
        }}
        onDrop={(e) => {
          if (!onFileSelect || isStreaming) return;
          e.preventDefault();
          e.stopPropagation();
          onFileSelect(e.dataTransfer.files);
        }}
      >
        {slashOverlay}
        {hasChips && (
          <div className="flex flex-wrap gap-1.5 px-4 pt-3 pb-1">
            {selectedSkills.map((s) => (
              <span
                key={s.name}
                className="inline-flex items-center gap-1 rounded-md border border-border bg-muted px-2.5 py-1 font-mono text-xs font-semibold text-foreground"
              >
                /{s.name}
                <button
                  type="button"
                  onClick={() => removeSkill(s.name)}
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
                    "inline-flex items-center gap-1.5 text-xs font-mono rounded-md px-3 py-1 max-w-48 border",
                    a.uploading
                      ? "bg-muted/50 text-muted-foreground border-border"
                      : "bg-muted text-muted-foreground border-border",
                  )}
                >
                  {a.uploading ? (
                    <div className="w-3 h-3 border border-muted-foreground/30 border-t-muted-foreground rounded-full animate-spin shrink-0" />
                  ) : (
                    <Paperclip className="w-3 h-3 shrink-0 text-muted-foreground" />
                  )}
                  <span className="truncate">{a.name}</span>
                  {!a.uploading && onRemoveAttachment && (
                    <button
                      onClick={() => onRemoveAttachment(i)}
                      className="text-muted-foreground hover:text-foreground cursor-pointer shrink-0 font-semibold ml-0.5"
                    >
                      ×
                    </button>
                  )}
                </span>
              ))}
          </div>
        )}
        <div className="relative min-w-0">
          <textarea
            ref={taRef}
            value={value}
            onChange={(e) => handleChange(e.target.value)}
            onKeyDown={(e) => {
              if (slashOpen) {
                if (e.key === "ArrowDown") {
                  e.preventDefault();
                  const next = (slashIndex + 1) % slashCandidates.length;
                  setSlashIndex(next);
                  slashListRef.current
                    ?.querySelector(`[data-index="${next}"]`)
                    ?.scrollIntoView({ block: "nearest" });
                  return;
                }
                if (e.key === "ArrowUp") {
                  e.preventDefault();
                  const next = (slashIndex - 1 + slashCandidates.length) % slashCandidates.length;
                  setSlashIndex(next);
                  slashListRef.current
                    ?.querySelector(`[data-index="${next}"]`)
                    ?.scrollIntoView({ block: "nearest" });
                  return;
                }
                if (e.key === "Enter" || e.key === "Tab") {
                  e.preventDefault();
                  insertSkill(slashCandidates[slashIndex]);
                  return;
                }
                if (e.key === "Escape") {
                  e.preventDefault();
                  setSlashQuery(null);
                  return;
                }
              }
              if (e.key === "Enter" && !e.shiftKey) {
                e.preventDefault();
                handleSend();
              }
            }}
            onInput={(e) => {
              const el = e.currentTarget;
              el.style.height = "auto";
              el.style.height = Math.min(el.scrollHeight, 160) + "px";
            }}
            onPaste={(e) => {
              if (!onFileSelect || isStreaming) return;
              const files = e.clipboardData.files;
              if (files.length > 0) {
                e.preventDefault();
                onFileSelect(files);
              }
            }}
            placeholder={placeholder}
            className="w-full min-w-0 resize-none overflow-y-auto border-0 bg-transparent px-4 pt-3 pb-1.5 pr-12 text-sm leading-relaxed text-foreground placeholder:text-muted-foreground focus:outline-none"
            style={{ minHeight: 40, maxHeight: 160 }}
            rows={1}
            disabled={disabled ?? isStreaming}
          />
          <div className="absolute bottom-1.5 right-2">
            {isStreaming && onStop ? (
              <Button variant="destructive-outline" size="sm" onClick={onStop}>
                <div className="w-2 h-2 bg-destructive rounded-xs" />
                <span>{t("sessions.composer.stop")}</span>
              </Button>
            ) : (
              <Button
                size="icon-sm"
                disabled={!canSend}
                onClick={handleSend}
                title={t("sessions.composer.sendMessage")}
              >
                <ArrowUp className="size-4 stroke-[2.5]" />
              </Button>
            )}
          </div>
        </div>
        <div className="flex min-w-0 items-center gap-1.5 px-2 pb-0.5">
          {!isStreaming && onFileSelect && (
            <Button
              variant="ghost"
              size="icon-xs"
              onClick={() => fileInputRef.current?.click()}
              title={t("sessions.composer.attachFiles")}
            >
              <Paperclip className="size-3.5" />
            </Button>
          )}
          {!isStreaming && (
            <span className="min-w-0 truncate font-mono text-xs text-muted-foreground select-none">
              {skills && skills.length > 0
                ? t("sessions.transcript.sendHintSkills")
                : t("sessions.transcript.sendHint")}
            </span>
          )}
          {isStreaming && (
            <span className="text-xs font-mono text-info select-none animate-pulse">
              {t("sessions.transcript.generating")}
            </span>
          )}
        </div>
      </div>
    </div>
  );
}
