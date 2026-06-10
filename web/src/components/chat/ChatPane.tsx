import type { ReactNode } from "react";

interface Props {
  transcript: ReactNode;
  composer?: ReactNode;
  banner?: ReactNode;
}

export function ChatPane({ transcript, composer, banner }: Props) {
  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-hidden bg-background">
      {banner}
      {transcript}
      {composer}
    </div>
  );
}
