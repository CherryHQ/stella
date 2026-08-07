import { MessageCircle } from "lucide-react";
import { siDiscord, siQq, siTelegram, siWechat } from "simple-icons";

/**
 * Chat platform identity — the brand mark and display name shared by every
 * surface that lists channels (settings' channel manager, an agent's channels
 * tab). Brand names are proper nouns, so they are not translated.
 */
export const PLATFORM_LABELS: Record<string, string> = {
  telegram: "Telegram",
  qq: "QQ",
  feishu: "Feishu",
  weixin: "Weixin",
  discord: "Discord",
};

export function platformLabel(type: string, fallback = ""): string {
  return PLATFORM_LABELS[type] || fallback || type;
}

const PLATFORM_ICON_PATHS: Record<string, string> = {
  telegram: siTelegram.path,
  qq: siQq.path,
  weixin: siWechat.path,
  discord: siDiscord.path,
};

/**
 * A brand mark for known chat platforms, else a generic message glyph (e.g.
 * Feishu, which simple-icons doesn't carry).
 */
export function PlatformIcon({ type }: { type: string }) {
  const path = PLATFORM_ICON_PATHS[type];
  if (!path) return <MessageCircle className="size-4" />;
  return (
    <svg viewBox="0 0 24 24" className="size-4" fill="currentColor" aria-hidden="true">
      <path d={path} />
    </svg>
  );
}
