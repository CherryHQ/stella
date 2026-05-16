import { useResizableWidth } from "@/hooks/use-resizable-width";

const DEFAULT_WIDTH = 480;
const MIN_WIDTH = 340;
const MAX_WIDTH = 720;

export function ResizableSidePanel({ children }: { children: React.ReactNode }) {
  const { width, onResizeStart } = useResizableWidth(DEFAULT_WIDTH, MIN_WIDTH, MAX_WIDTH);

  return (
    <div
      className="relative flex-shrink-0 border-l border-border/60 flex flex-col overflow-hidden bg-background"
      style={{ width, minWidth: MIN_WIDTH }}
    >
      <div
        onMouseDown={onResizeStart}
        className="absolute left-0 top-0 bottom-0 w-1 cursor-col-resize z-10 hover:bg-primary/10 active:bg-primary/20 transition-colors"
      />
      {children}
    </div>
  );
}
