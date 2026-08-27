import { MessageCircle } from "lucide-react";
import { siDiscord, siQq, siTelegram, siWechat } from "simple-icons";

/**
 * Chat platform identity — the brand mark and display name shared by every
 * surface that lists channels (settings' channel manager, an agent's channels
 * tab). Brand names are proper nouns, so they are not translated.
 */
export const PLATFORM_LABELS = {
  telegram: "Telegram",
  discord: "Discord",
  qq: "QQ",
  feishu: "Feishu",
  dingtalk: "DingTalk",
  weixin: "Weixin",
} satisfies Record<string, string>;

export function platformLabel(type: string, fallback = ""): string {
  // SAFETY: unknown platform names use the caller fallback or the original name.
  return PLATFORM_LABELS[type as keyof typeof PLATFORM_LABELS] || fallback || type;
}

const PLATFORM_ICON_PATHS = {
  telegram: siTelegram.path,
  discord: siDiscord.path,
  qq: siQq.path,
  weixin: siWechat.path,
} satisfies Record<string, string>;

/**
 * A brand mark for known chat platforms, else a generic message glyph (e.g.
 * Feishu, which simple-icons doesn't carry).
 */
export function PlatformIcon({ type }: { type: string }) {
  // SAFETY: unknown platform names intentionally render the generic glyph.
  const path = PLATFORM_ICON_PATHS[type as keyof typeof PLATFORM_ICON_PATHS];
  if (!path) return <MessageCircle className="size-4" />;
  return (
    <svg viewBox="0 0 24 24" className="size-4" fill="currentColor" aria-hidden="true">
      <path d={path} />
    </svg>
  );
}
