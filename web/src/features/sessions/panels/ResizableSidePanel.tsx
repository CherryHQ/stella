import { useMediaQuery } from "@/hooks/use-media-query";
import { useResizableWidth } from "@/hooks/use-resizable-width";

const DEFAULT_WIDTH = 480;
const MIN_WIDTH = 340;
const MAX_WIDTH = 720;

export function ResizableSidePanel({ children }: { children: React.ReactNode }) {
  const { width, onResizeStart } = useResizableWidth(DEFAULT_WIDTH, MIN_WIDTH, MAX_WIDTH);
  const isDesktop = useMediaQuery("(min-width: 768px)");

  return (
    <div
      className={
        isDesktop
          ? "relative flex-shrink-0 border-l border-border/60 flex flex-col overflow-hidden bg-background"
          : "relative flex min-h-0 flex-1 flex-col overflow-hidden bg-background"
      }
      style={isDesktop ? { width, minWidth: MIN_WIDTH } : undefined}
    >
      <div
        onMouseDown={onResizeStart}
        className="absolute left-0 top-0 bottom-0 z-10 hidden w-1 cursor-col-resize transition-colors hover:bg-primary/10 active:bg-primary/20 md:block"
      />
      {children}
    </div>
  );
}
