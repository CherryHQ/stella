import { useState } from "react";
import { FileText } from "lucide-react";
import { Link } from "@tanstack/react-router";
import { useI18n } from "@/lib/i18n";
import { formatTime } from "@/lib/time";
import { cn } from "@/lib/utils";
import type { ContentBlock } from "@/lib/types";
import { Badge } from "@/components/ui/badge";
import { CopyButton, REVEAL_ON_HOVER } from "./CopyButton";
import {
  attachmentDisplayName,
  basename,
  replaceUUIDMentions,
  userMessageRenderInput,
  workspaceFileURL,
} from "./utils";
import { AttachmentPreview } from "./AttachmentPreview";

export interface UserMessageProps {
  msg: {
    content?: string;
    blocks?: ContentBlock[];
    timestamp?: string;
    token_count?: number;
  };
  agentId?: string;
  sessionId?: string;
  agentNames?: Map<string, string>;
  sameRoleAsPrev?: boolean;
  showTimestamp?: boolean;
  actorType?: "human" | "agent" | "system";
  actorId?: string;
  sourceSessionId?: string;
}

export function UserMessage({
  msg,
  agentId,
  sessionId,
  agentNames,
  sameRoleAsPrev,
  showTimestamp,
  actorType,
  actorId,
  sourceSessionId,
}: UserMessageProps) {
  const { t } = useI18n();
  const [previewPath, setPreviewPath] = useState<string | null>(null);
  const { canonicalBlocks, hasCanonicalImage, text, images, otherFiles } = userMessageRenderInput(
    msg,
    agentNames,
  );
  const nonHuman = actorType === "agent" || actorType === "system";
  const actorLabel =
    actorType === "agent"
      ? (agentNames?.get(actorId ?? "") ?? actorId ?? t("chat.agentMessage"))
      : actorType === "system"
        ? t("chat.systemMessage")
        : t("chat.you");

  return (
    <div className="group w-full min-w-0 flex flex-col items-end gap-1.5">
      {!sameRoleAsPrev && (
        <div className="flex items-center gap-2 mb-0.5">
          <span className="text-xs font-semibold text-foreground">{actorLabel}</span>
          {sourceSessionId && (
            <Badge
              variant="secondary"
              size="sm"
              title={sourceSessionId}
              render={
                agentId ? (
                  <Link
                    to="/agents/$agentId/sessions/$sessionId"
                    params={{ agentId, sessionId: sourceSessionId }}
                  />
                ) : undefined
              }
            >
              {t("chat.fromSession")}
            </Badge>
          )}
          <span className="grid size-5 place-items-center rounded-full bg-foreground/15 text-xs font-semibold text-foreground shrink-0">
            {nonHuman ? (actorType === "agent" ? "A" : "S") : "Y"}
          </span>
        </div>
      )}
      <div className="w-full min-w-0 flex flex-col items-end gap-2">
        {hasCanonicalImage
          ? canonicalBlocks?.map((block, index) =>
              block.type === "text" ? (
                <div
                  key={`text-${index}`}
                  className="min-w-0 max-w-[85%] break-words rounded-2xl rounded-tr-md border border-border bg-secondary px-4 py-2.5 text-sm leading-relaxed whitespace-pre-wrap text-foreground font-sans text-left"
                >
                  {renderMentionedText(replaceUUIDMentions(block.text, agentNames), agentNames)}
                </div>
              ) : (
                <a
                  key={`image-${block.media_id}-${index}`}
                  href={block.url}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="block overflow-hidden rounded-lg border border-border hover:border-primary/40 transition-colors"
                >
                  <img
                    src={block.url}
                    alt="Image attachment"
                    className="max-h-56 max-w-full object-cover"
                    loading="lazy"
                  />
                </a>
              ),
            )
          : text && (
              <div className="min-w-0 max-w-[85%] break-words rounded-2xl rounded-tr-md border border-border bg-secondary px-4 py-2.5 text-sm leading-relaxed whitespace-pre-wrap text-foreground font-sans text-left">
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
                    alt={attachmentDisplayName(path)}
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
              <button
                key={i}
                type="button"
                onClick={() => setPreviewPath(path)}
                title={basename(path)}
                className="inline-flex cursor-pointer items-center gap-2 rounded-md border border-border bg-card px-3 py-1.5 text-xs text-secondary-foreground transition-colors hover:bg-muted"
              >
                <FileText className="size-3.5 shrink-0 text-muted-foreground" />
                <span className="max-w-48 truncate font-medium">{attachmentDisplayName(path)}</span>
              </button>
            ))}
          </div>
        )}
        {agentId && sessionId && (
          <AttachmentPreview
            agentId={agentId}
            sessionId={sessionId}
            path={previewPath}
            onClose={() => setPreviewPath(null)}
          />
        )}
        {(hasCanonicalImage || text || (showTimestamp && msg.timestamp)) && (
          <div
            className={cn(
              "flex items-center gap-2 text-xs font-mono text-muted-foreground",
              REVEAL_ON_HOVER,
            )}
          >
            {showTimestamp && msg.timestamp && <span>{formatTime(msg.timestamp)}</span>}
            {hasCanonicalImage ? (
              <CopyButton
                text={(canonicalBlocks ?? [])
                  .filter(
                    (block): block is Extract<ContentBlock, { type: "text" }> =>
                      block.type === "text",
                  )
                  .map((block) => block.text)
                  .join("\n")}
                className="-mr-1.5"
              />
            ) : (
              text && <CopyButton text={text} className="-mr-1.5" />
            )}
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
