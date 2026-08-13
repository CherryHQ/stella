import { useCallback, useEffect, useRef, useState } from "react";
import QRCode from "qrcode";
import { generateLinkCode, pollWeixinQrStatus, startWeixinQr } from "@/lib/api-client/sdk.gen";
import { apiErrorMessage } from "@/lib/api-error";
import { useI18n } from "@/lib/i18n";

/**
 * Platforms whose accounts are linked by sending `/link <code>` to the bot.
 * The backend rejects any other platform on POST /api/users/me/link-code, so
 * the UI must not offer the button for one.
 */
export const LINK_CODE_PLATFORMS = new Set(["telegram", "discord", "qq", "feishu", "dingtalk"]);

/** Weixin links by QR scan instead of a code. */
export const QR_PLATFORM = "weixin";

export function weixinQrStatusVariant(
  status: string,
): "warning" | "info" | "success" | "error" | "secondary" {
  if (status === "waiting") return "warning";
  if (status === "scaned") return "info";
  if (status === "confirmed") return "success";
  if (status === "expired") return "error";
  return "secondary";
}

export interface AccountLink {
  /** Platform the pending code or QR belongs to; empty when nothing is pending. */
  platform: string;
  code: string;
  generating: boolean;
  /** Data URL of the Weixin QR image, empty when there is none to show. */
  qrUrl: string;
  qrStatus: string;
  qrPolling: boolean;
  generateCode: (platform: string) => Promise<void>;
  copyCode: () => void;
  startQr: () => Promise<void>;
  reset: () => void;
}

/**
 * Linking *your* chat account to your Stella user: a one-shot `/link` code for
 * code platforms, a polled QR for Weixin. Deliberately headless — the channel
 * settings page and an agent's channels tab render it differently but must not
 * own two copies of the polling lifecycle.
 *
 * Only one link attempt is ever pending: starting one clears the other, so the
 * caller can key its rendering off {@link AccountLink.platform}.
 */
export function useAccountLink({
  notify,
  onLinked,
}: {
  notify: (text: string, kind?: "success" | "error") => void;
  /** Fired once a QR scan is confirmed, so the caller can refresh identities. */
  onLinked?: () => void;
}): AccountLink {
  const { t } = useI18n();
  const [platform, setPlatform] = useState("");
  const [code, setCode] = useState("");
  const [generating, setGenerating] = useState(false);
  const [qrUrl, setQrUrl] = useState("");
  const [qrStatus, setQrStatus] = useState("");
  const [qrPolling, setQrPolling] = useState(false);
  const intervalRef = useRef<ReturnType<typeof setInterval> | null>(null);

  // Read through a ref so a caller's inline callback never restarts polling.
  const onLinkedRef = useRef(onLinked);
  onLinkedRef.current = onLinked;

  const stopPolling = useCallback(() => {
    if (intervalRef.current) {
      clearInterval(intervalRef.current);
      intervalRef.current = null;
    }
    setQrPolling(false);
  }, []);

  useEffect(
    () => () => {
      if (intervalRef.current) clearInterval(intervalRef.current);
    },
    [],
  );

  const reset = useCallback(() => {
    setPlatform("");
    setCode("");
    setQrUrl("");
    setQrStatus("");
    stopPolling();
  }, [stopPolling]);

  const generateCode = useCallback(
    async (target: string) => {
      setGenerating(true);
      setPlatform(target);
      setCode("");
      setQrUrl("");
      setQrStatus("");
      stopPolling();
      try {
        const { data } = await generateLinkCode({ body: { platform: target }, throwOnError: true });
        setCode(data.code);
      } catch (e) {
        notify(apiErrorMessage(e, t("channels.linkCodeFailed")), "error");
      } finally {
        setGenerating(false);
      }
    },
    [notify, stopPolling, t],
  );

  const copyCode = useCallback(() => {
    void navigator.clipboard.writeText("/link " + code);
    notify(t("common.copied"));
  }, [code, notify, t]);

  const pollStatus = useCallback(
    async (qrCode: string) => {
      if (!qrCode) return;
      try {
        const { data } = await pollWeixinQrStatus({
          query: { qrcode: qrCode },
          throwOnError: true,
        });
        if (data.status) setQrStatus(data.status);
        if (data.status === "confirmed") {
          stopPolling();
          setQrUrl("");
          notify(t("channels.weixinLinked"));
          onLinkedRef.current?.();
        } else if (data.status === "expired") {
          stopPolling();
        }
      } catch {
        // A single failed poll is not fatal: the interval retries.
      }
    },
    [notify, stopPolling, t],
  );

  const startQr = useCallback(async () => {
    setPlatform(QR_PLATFORM);
    setCode("");
    setQrUrl("");
    setQrStatus("");
    stopPolling();
    setQrPolling(true);
    try {
      const { data } = await startWeixinQr({ throwOnError: true });
      const qrCode = data.qrcode || "";
      const imgContent = data.qrcode_img_content || "";
      if (imgContent) {
        setQrUrl(await QRCode.toDataURL(imgContent, { width: 256, margin: 2 }));
      }
      setQrStatus("waiting");
      intervalRef.current = setInterval(() => void pollStatus(qrCode), 3000);
    } catch (e) {
      notify(apiErrorMessage(e, t("channels.qrFailed")), "error");
      setQrPolling(false);
    }
  }, [notify, pollStatus, stopPolling, t]);

  return {
    platform,
    code,
    generating,
    qrUrl,
    qrStatus,
    qrPolling,
    generateCode,
    copyCode,
    startQr,
    reset,
  };
}
