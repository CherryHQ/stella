import { useEffect, useState } from "react";

let seq = 0;

function isDark(): boolean {
  return document.documentElement.classList.contains("dark");
}

export function Mermaid({ chart }: { chart: string }) {
  const [svg, setSvg] = useState("");
  const [error, setError] = useState<string>();

  useEffect(() => {
    let active = true;
    void (async () => {
      try {
        const mermaid = (await import("mermaid")).default;
        mermaid.initialize({
          startOnLoad: false,
          securityLevel: "strict",
          theme: isDark() ? "dark" : "default",
        });
        const { svg } = await mermaid.render(`mermaid-${seq++}`, chart);
        if (active) setSvg(svg);
      } catch (e) {
        if (active) setError(e instanceof Error ? e.message : String(e));
      }
    })();
    return () => {
      active = false;
    };
  }, [chart]);

  if (error) {
    return (
      <pre className="bg-muted rounded-lg p-4 my-4 overflow-x-auto text-sm text-destructive-foreground">
        {error}
        {"\n\n"}
        {chart}
      </pre>
    );
  }

  return (
    // eslint-disable-next-line react/no-danger
    <div
      className="my-6 flex justify-center [&_svg]:max-w-full"
      dangerouslySetInnerHTML={{ __html: svg }}
    />
  );
}
