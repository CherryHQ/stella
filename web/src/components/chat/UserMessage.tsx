import { FileText } from "lucide-react";
import { useI18n } from "@/lib/i18n";
import { formatTime } from "@/lib/time";
import { cn } from "@/lib/utils";
import { CopyButton, REVEAL_ON_HOVER } from "./CopyButton";
import {
  replaceUUIDMentions,
  extractUserText,
  parseFileRefs,
  isImagePath,
  workspaceFileURL,
  basename,
} from "./utils";

export interface UserMessageProps {
  msg: {
    content?: string;
    timestamp?: string;
    token_count?: number;
  };
  agentId?: string;
  sessionId?: string;
  agentNames?: Map<string, string>;
  sameRoleAsPrev?: boolean;
  showTimestamp?: boolean;
}

export function UserMessage({
  msg,
  agentId,
  sessionId,
  agentNames,
  sameRoleAsPrev,
  showTimestamp,
}: UserMessageProps) {
  const { t } = useI18n();
  const displayContent = replaceUUIDMentions(extractUserText(msg), agentNames);
  const { files, text } = parseFileRefs(displayContent);
  const images = files.filter(isImagePath);
  const otherFiles = files.filter((f) => !isImagePath(f));

  return (
    <div className="group w-full min-w-0 flex flex-col items-end gap-1.5">
      {!sameRoleAsPrev && (
        <div className="flex items-center gap-2 mb-0.5">
          <span className="text-xs font-semibold text-foreground">{t("chat.you")}</span>
          <span className="grid size-5 place-items-center rounded-full bg-foreground/15 text-xs font-semibold text-foreground shrink-0">
            Y
          </span>
        </div>
      )}
      <div className="w-full min-w-0 flex flex-col items-end gap-2">
        {text && (
          <div className="min-w-0 max-w-[85%] break-words rounded-2xl rounded-tr-md border border-border bg-secondary px-4 py-2.5 text-[15px] leading-relaxed whitespace-pre-wrap text-foreground font-sans text-left">
            {renderMentionedText(text, agentNames)}
          </div>
        )}

        {agentId && sessionId && images.length > 0 && (
          <div className="flex flex-wrap gap-2 pt-1">
            {images.map((path, i) => {
              const url = workspaceFileURL(agentId, sessionId, path);
              return (
                <a
                  key={i}
                  href={url}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="block overflow-hidden rounded-lg border border-border hover:border-primary/40 transition-colors"
                >
                  <img
                    src={url}
                    alt={basename(path)}
                    className="max-h-56 max-w-full object-cover"
                    loading="lazy"
                  />
                </a>
              );
            })}
          </div>
        )}

        {agentId && sessionId && otherFiles.length > 0 && (
          <div className="flex flex-wrap gap-2 pt-1">
            {otherFiles.map((path, i) => (
              <a
                key={i}
                href={workspaceFileURL(agentId, sessionId, path)}
                target="_blank"
                rel="noopener noreferrer"
                className="inline-flex items-center gap-2 rounded-md border border-border bg-card hover:bg-muted transition-colors px-3 py-1.5 text-xs text-secondary-foreground"
              >
                <FileText className="size-3.5 text-muted-foreground shrink-0" />
                <span className="truncate max-w-48 font-medium">{basename(path)}</span>
              </a>
            ))}
          </div>
        )}
        {(text || (showTimestamp && msg.timestamp)) && (
          <div
            className={cn(
              "flex items-center gap-2 text-xs font-mono text-muted-foreground/60",
              REVEAL_ON_HOVER,
            )}
          >
            {showTimestamp && msg.timestamp && <span>{formatTime(msg.timestamp)}</span>}
            {text && <CopyButton text={text} className="-mr-1.5" />}
          </div>
        )}
      </div>
    </div>
  );
}

function renderMentionedText(text: string, agentNames?: Map<string, string>) {
  if (!agentNames || agentNames.size === 0) return text;

  const names = new Set<string>();
  for (const [id, name] of agentNames) {
    names.add(id.toLowerCase());
    if (name) names.add(name.toLowerCase());
  }

  return text.split(/(@[^\s@]+)/g).map((part, index) => {
    if (!part.startsWith("@") || !names.has(part.slice(1).toLowerCase())) {
      return part;
    }
    return (
      <span key={`${part}-${index}`} className="rounded-md bg-muted px-1 py-0.5 text-foreground">
        {part}
      </span>
    );
  });
}
