import type { MouseEvent } from "react";
import { Button } from "@/components/ui/button";

export function OAuthConnectionActions({
  connected,
  needsReconnect = false,
  loading = false,
  disabled = false,
  size = "sm",
  connectLabel,
  reconnectLabel,
  disconnectLabel,
  onConnect,
  onDisconnect,
  stopPropagation = false,
}: {
  connected: boolean;
  needsReconnect?: boolean;
  loading?: boolean;
  disabled?: boolean;
  size?: "xs" | "sm";
  connectLabel: string;
  reconnectLabel: string;
  disconnectLabel: string;
  onConnect: () => void;
  onDisconnect: () => void;
  stopPropagation?: boolean;
}) {
  const invoke = (action: () => void) => (event: MouseEvent<HTMLButtonElement>) => {
    if (stopPropagation) event.stopPropagation();
    action();
  };

  return (
    <div className="flex flex-wrap items-center gap-2">
      {(!connected || needsReconnect) && (
        <Button size={size} loading={loading} disabled={disabled} onClick={invoke(onConnect)}>
          {needsReconnect ? reconnectLabel : connectLabel}
        </Button>
      )}
      {connected && (
        <Button
          size={size}
          variant="destructive"
          disabled={disabled}
          onClick={invoke(onDisconnect)}
        >
          {disconnectLabel}
        </Button>
      )}
    </div>
  );
}
