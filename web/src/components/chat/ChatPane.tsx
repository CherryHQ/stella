import type { ReactNode } from "react";

interface Props {
  transcript: ReactNode;
  composer?: ReactNode;
  banner?: ReactNode;
  /** Rendered between the transcript and composer (e.g. run-level error notice). */
  notice?: ReactNode;
}

export function ChatPane({ transcript, composer, banner, notice }: Props) {
  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden bg-background">
      {banner}
      {transcript}
      {notice}
      {composer}
    </div>
  );
}
