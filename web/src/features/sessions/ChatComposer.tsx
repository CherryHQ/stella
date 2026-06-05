import type { ReactNode, RefObject } from "react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ArrowUp, Paperclip, X } from "lucide-react";
import { cn } from "@/lib/utils";

export interface Attachment {
  name: string;
  path: string;
  uploading: boolean;
}

export interface ComposerSkill {
  name: string;
  description: string;
}

interface Props {
  value: string;
  onChange: (value: string) => void;
  onSend: () => void;
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
  value,
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
  const fileInputRef = useRef<HTMLInputElement>(null);
  const internalTextareaRef = useRef<HTMLTextAreaElement | null>(null);
  const taRef = textareaRef ?? internalTextareaRef;

  const [selectedSkills, setSelectedSkills] = useState<ComposerSkill[]>([]);

  const hasAttachments = attachments && attachments.length > 0;
  const canSend =
    (value.trim() ||
      selectedSkills.length > 0 ||
      (hasAttachments && attachments.some((a) => !a.uploading))) &&
    !attachments?.some((a) => a.uploading);

  const handleSend = useCallback(() => {
    if (selectedSkills.length > 0) {
      const prefix = selectedSkills.map((s) => `/${s.name}`).join(" ");
      const full = value.trim() ? `${prefix} ${value}` : prefix;
      onChange(full);
      setSelectedSkills([]);
      setTimeout(() => onSend(), 0);
    } else {
      onSend();
    }
  }, [selectedSkills, value, onChange, onSend]);

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
      onChange(val);
      detectSlash(val);
    },
    [onChange, detectSlash],
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
      onChange(newVal);
      setSelectedSkills((prev) => [...prev, skill]);
      setSlashQuery(null);
      textarea.focus();
    },
    [value, onChange, taRef],
  );

  const removeSkill = useCallback((name: string) => {
    setSelectedSkills((prev) => prev.filter((s) => s.name !== name));
  }, []);

  const slashOverlay = slashOpen ? (
    <div
      ref={slashListRef}
      className="absolute bottom-full left-0 right-0 z-10 mb-1 max-h-48 overflow-y-auto rounded-lg border border-border bg-popover shadow-lg"
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
              i === slashIndex ? "bg-primary/5" : "",
            )}
          >
            <code
              className={cn(
                "shrink-0 rounded px-1.5 py-0.5 font-mono text-xs font-semibold",
                i === slashIndex ? "bg-primary/10 text-primary" : "bg-muted text-primary",
              )}
            >
              /{s.name}
            </code>
            <span
              className={cn(
                "truncate text-xs leading-tight",
                i === slashIndex ? "text-foreground/70" : "text-muted-foreground",
              )}
            >
              {s.description}
            </span>
          </button>
        ))}
      </div>
    </div>
  ) : null;

  const hasChips = (hasAttachments && attachments.length > 0) || selectedSkills.length > 0;

  return (
    <div className="relative flex-shrink-0 px-4 pt-2 pb-3 sm:px-8">
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
          "relative mx-auto max-w-3xl rounded-xl border bg-card transition-all duration-120 flex flex-col p-1.5 shadow-none",
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
                className="inline-flex items-center gap-1 rounded-md border border-primary/20 bg-primary/5 px-2.5 py-1 font-mono text-[11px] font-semibold text-primary"
              >
                /{s.name}
                <button
                  type="button"
                  onClick={() => removeSkill(s.name)}
                  className="ml-0.5 shrink-0 cursor-pointer text-primary/40 transition-colors hover:text-primary"
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
                    "inline-flex items-center gap-1.5 text-[11px] font-mono rounded-md px-3 py-1 max-w-48 border",
                    a.uploading
                      ? "bg-muted/50 text-muted-foreground/50 border-border"
                      : "bg-primary/5 text-primary border-primary/20",
                  )}
                >
                  {a.uploading ? (
                    <div className="w-3 h-3 border border-muted-foreground/30 border-t-muted-foreground rounded-full animate-spin shrink-0" />
                  ) : (
                    <Paperclip className="w-3 h-3 shrink-0 text-primary/70" />
                  )}
                  <span className="truncate">{a.name}</span>
                  {!a.uploading && onRemoveAttachment && (
                    <button
                      onClick={() => onRemoveAttachment(i)}
                      className="text-muted-foreground/50 hover:text-foreground cursor-pointer shrink-0 font-bold ml-0.5"
                    >
                      ×
                    </button>
                  )}
                </span>
              ))}
          </div>
        )}
        <div className="relative">
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
            className="w-full resize-none overflow-y-auto border-0 bg-transparent px-4 pt-3 pb-1.5 pr-12 text-[15px] leading-relaxed focus:outline-none placeholder:text-muted-foreground/60 text-foreground"
            style={{ minHeight: 40, maxHeight: 160 }}
            rows={1}
            disabled={disabled ?? isStreaming}
          />
          <div className="absolute bottom-1.5 right-2">
            {isStreaming && onStop ? (
              <button
                type="button"
                onClick={onStop}
                className="text-destructive hover:bg-destructive/10 bg-destructive/5 border border-destructive/25 font-semibold text-xs rounded-lg px-3 h-7 transition-colors flex items-center gap-1.5 cursor-pointer"
              >
                <div className="w-2 h-2 bg-destructive rounded-xs" />
                <span>Stop</span>
              </button>
            ) : (
              <button
                type="button"
                disabled={!canSend}
                onClick={handleSend}
                className={cn(
                  "w-7 h-7 rounded-lg flex items-center justify-center transition-colors cursor-pointer",
                  !canSend
                    ? "bg-muted text-muted-foreground/30 cursor-not-allowed"
                    : "bg-primary text-primary-foreground hover:bg-primary-hover",
                )}
                title="Send message"
              >
                <ArrowUp className="w-4 h-4 stroke-[2.5]" />
              </button>
            )}
          </div>
        </div>
        <div className="flex items-center gap-1.5 px-2 pb-0.5">
          {!isStreaming && onFileSelect && (
            <button
              type="button"
              onClick={() => fileInputRef.current?.click()}
              className="text-muted-foreground hover:text-foreground hover:bg-muted transition-colors p-1 rounded-md w-6 h-6 flex items-center justify-center cursor-pointer"
              title="Attach files"
            >
              <Paperclip className="w-3.5 h-3.5" />
            </button>
          )}
          {!isStreaming && (
            <span className="text-[9px] font-mono text-muted-foreground/30 select-none">
              ↵ send · ⇧↵ new line{skills && skills.length > 0 ? " · / skills" : ""}
            </span>
          )}
          {isStreaming && (
            <span className="text-[9px] font-mono text-primary/70 select-none animate-pulse">
              generating…
            </span>
          )}
        </div>
      </div>
    </div>
  );
}
