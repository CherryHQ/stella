import { AlertCircle } from "lucide-react";

interface AuthLayoutProps {
  subtitle: string;
  error?: string;
  children: React.ReactNode;
}

export function AuthLayout({ subtitle, error, children }: AuthLayoutProps) {
  return (
    <div className="min-h-screen w-full flex items-center justify-center bg-background relative overflow-hidden">
      <div className="absolute top-[-20%] left-[-20%] w-[60%] h-[60%] rounded-full bg-primary/10 blur-[150px] pointer-events-none" />
      <div className="absolute bottom-[-20%] right-[-20%] w-[60%] h-[60%] rounded-full bg-primary/10 blur-[150px] pointer-events-none" />
      <div className="absolute inset-0 bg-[linear-gradient(to_right,var(--border)_1px,transparent_1px),linear-gradient(to_bottom,var(--border)_1px,transparent_1px)] bg-[size:24px_24px] opacity-30 pointer-events-none [mask-image:radial-gradient(ellipse_60%_50%_at_50%_50%,var(--foreground)_80%,transparent_100%)]" />

      <div className="w-full max-w-[420px] mx-4 rounded-2xl border border-border bg-card backdrop-blur-xl p-8 relative z-10 transition-colors duration-120 hover:border-border">
        <div className="text-center mb-8">
          <div className="inline-flex items-center justify-center p-3 rounded-2xl bg-primary/5 border border-primary/10 mb-4 shadow-inner">
            <img
              src="/stella-monogram.svg"
              alt="Stella"
              width={40}
              height={40}
              className="rounded-lg animate-pulse"
            />
          </div>
          <h1 className="font-semibold text-primary text-4xl tracking-tight select-none">stella</h1>
          <p className="text-muted-foreground text-sm mt-2">{subtitle}</p>
        </div>

        {error && (
          <div className="mb-6 p-4 rounded-lg bg-destructive/10 border border-destructive/20 text-destructive text-sm flex items-start gap-3 animate-shake">
            <AlertCircle className="size-4 mt-0.5 shrink-0" />
            <span>{error}</span>
          </div>
        )}

        {children}
      </div>
    </div>
  );
}
