import { Streamdown } from "streamdown";
import { cn } from "@/lib/utils";

interface Props {
  content: string;
  className?: string;
  variant?: "default" | "card";
}

const variantClassName: Record<NonNullable<Props["variant"]>, string> = {
  default: "",
  card: "rounded-xl bg-muted/30 p-6 sm:p-8",
};

export function MarkdownPreview({ content, className, variant = "default" }: Props) {
  return (
    <div
      className={cn(
        "min-w-0 max-w-full overflow-hidden text-sm text-foreground",
        "[overflow-wrap:anywhere] [&_*]:min-w-0 [&_*]:max-w-full [&_a]:break-words [&_a]:[overflow-wrap:anywhere] [&_code]:break-words [&_code]:[overflow-wrap:anywhere] [&_li]:[overflow-wrap:anywhere] [&_p]:break-words [&_p]:[overflow-wrap:anywhere] [&_pre]:max-w-full [&_pre]:overflow-x-auto [&_table]:block [&_table]:max-w-full [&_table]:overflow-x-auto",
        variantClassName[variant],
        className,
      )}
    >
      <Streamdown>{content}</Streamdown>
    </div>
  );
}
