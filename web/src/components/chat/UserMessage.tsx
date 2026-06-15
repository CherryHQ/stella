import { FileText } from "lucide-react";
import { useI18n } from "@/lib/i18n";
import { formatTime } from "@/lib/time";
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
}

export function UserMessage({
  msg,
  agentId,
  sessionId,
  agentNames,
  sameRoleAsPrev,
}: UserMessageProps) {
  const { t } = useI18n();
  const displayContent = replaceUUIDMentions(extractUserText(msg), agentNames);
  const { files, text } = parseFileRefs(displayContent);
  const images = files.filter(isImagePath);
  const otherFiles = files.filter((f) => !isImagePath(f));

  return (
    <div className="w-full min-w-0 flex flex-col gap-1.5">
      {!sameRoleAsPrev && (
        <div className="flex items-center gap-2 mb-0.5">
          <span className="grid size-5 place-items-center rounded-full bg-foreground/15 text-xs font-semibold text-foreground shrink-0">
            Y
          </span>
          <span className="text-xs font-semibold text-foreground">{t("chat.you")}</span>
          {msg.timestamp && (
            <span className="font-mono text-xs text-muted-foreground/50">
              {formatTime(msg.timestamp)}
            </span>
          )}
        </div>
      )}
      <div className="pl-7 w-full min-w-0 flex flex-col items-start gap-3">
        {text && (
          <div className="min-w-0 break-words text-[15px] leading-relaxed whitespace-pre-wrap text-foreground/90 font-sans">
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
