import { createFileRoute, Link } from "@tanstack/react-router";
import { HomeLayout } from "fumadocs-ui/layouts/home";
import { baseOptions } from "@/lib/layout.shared";

export const Route = createFileRoute("/")({ component: Home });

const terminalLines = [
  { prompt: true, text: "anna onboard" },
  { prompt: false, text: "config created at ~/.anna/config.yaml" },
  { prompt: false, text: "opening setup at http://localhost:8080 ..." },
  { prompt: true, text: "anna chat" },
  { prompt: false, text: 'you: "summarize yesterday\'s conversation"' },
  { prompt: false, text: "anna: Yesterday you discussed migrating the" },
  { prompt: false, text: "      auth service to JWT tokens. Key decisions:" },
  { prompt: false, text: "      1. RS256 signing with key rotation ..." },
];

const features = [
  {
    label: "01",
    title: "Lossless memory",
    body: "DAG-based context compression. Conversations grow without bounds and without losing a single detail. Every thread, every tangent, preserved.",
  },
  {
    label: "02",
    title: "Multi-channel",
    body: "Terminal TUI, Telegram, QQ, Feishu. All channels share the same session and memory. Start a thought in your terminal, pick it up on Telegram.",
  },
  {
    label: "03",
    title: "Self-hosted",
    body: "Single Go binary + SQLite. Your machine, your API keys. Nothing leaves your network. Deploy with Docker, systemd, or just run the binary.",
  },
  {
    label: "04",
    title: "Built-in scheduler",
    body: "Scheduled tasks, heartbeat monitoring, and cross-channel notifications. anna works even when you're not talking to it.",
  },
];

function Home() {
  return (
    <HomeLayout {...baseOptions()}>
      <div className="home-page">
        <HeroSection />
        <FeaturesSection />
        <FooterCTA />
      </div>
    </HomeLayout>
  );
}

function HeroSection() {
  return (
    <section className="px-6 pt-28 pb-24 md:px-12 lg:px-20 max-w-7xl mx-auto">
      <div className="grid grid-cols-1 lg:grid-cols-2 gap-12 lg:gap-16 items-start">
        {/* Left — copy */}
        <div>
          <div className="animate-fade-up stagger-1">
            <p className="text-xs font-medium tracking-[0.2em] uppercase text-[var(--color-terra)] mb-8 font-[family-name:var(--font-mono)]">
              AI assistant / self-hosted
            </p>
          </div>
          <h1 className="animate-fade-up stagger-2 text-5xl md:text-6xl lg:text-[4.5rem] tracking-tight text-fd-foreground leading-[0.92] mb-10">
            Your assistant
            <br />
            that never
            <br />
            <span className="italic text-[var(--color-terra)]">forgets</span>
          </h1>
          <div className="animate-fade-up stagger-3 max-w-md">
            <p className="text-fd-muted-foreground text-base leading-relaxed mb-12">
              Single binary, lossless context management. Talk from your
              terminal or any messenger — anna remembers everything.
            </p>
          </div>
          <div className="animate-fade-up stagger-4 flex items-center gap-5">
            <Link
              to="/docs/$"
              params={{ _splat: "" }}
              className="inline-flex items-center gap-2 px-5 py-2.5 bg-[var(--color-terra)] text-white text-sm font-medium rounded-md hover:bg-[var(--color-terra-light)] transition-colors"
            >
              Read the docs
              <span aria-hidden="true">&rarr;</span>
            </Link>
            <a
              href="https://github.com/vaayne/anna"
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex items-center gap-2 text-fd-muted-foreground text-sm font-medium underline underline-offset-4 decoration-fd-border hover:text-fd-foreground hover:decoration-fd-foreground transition-colors"
            >
              Source on GitHub
            </a>
          </div>
        </div>

        {/* Right — terminal */}
        <div className="animate-fade-up stagger-5 lg:pt-8">
          <div className="bg-[var(--color-warm-900)] rounded-lg overflow-hidden ring-1 ring-white/[0.08]">
            <div className="flex items-center gap-1.5 px-4 py-3 border-b border-white/[0.06]">
              <div className="w-2.5 h-2.5 rounded-full bg-white/15" />
              <div className="w-2.5 h-2.5 rounded-full bg-white/15" />
              <div className="w-2.5 h-2.5 rounded-full bg-white/15" />
              <span className="ml-3 text-xs text-white/30 font-[family-name:var(--font-mono)]">
                terminal
              </span>
            </div>
            <div className="p-6 font-[family-name:var(--font-mono)] text-[13px] leading-7">
              {terminalLines.map((line, i) => (
                <div key={i} className={line.prompt ? "mt-3 first:mt-0" : ""}>
                  {line.prompt && (
                    <span className="text-[var(--color-terra-light)] select-none">
                      ${" "}
                    </span>
                  )}
                  <span
                    className={line.prompt ? "text-white/90" : "text-white/50"}
                  >
                    {line.text}
                  </span>
                </div>
              ))}
            </div>
          </div>
        </div>
      </div>
    </section>
  );
}

function FeaturesSection() {
  return (
    <section className="px-6 pt-20 pb-24 md:px-12 lg:px-20 max-w-7xl mx-auto border-t border-fd-border">
      <h2 className="text-3xl md:text-4xl tracking-tight text-fd-foreground mb-20">
        What makes anna different
      </h2>
      <div className="grid grid-cols-1 md:grid-cols-2 gap-x-20 gap-y-16">
        {features.map((f) => (
          <FeatureItem key={f.label} {...f} />
        ))}
      </div>
    </section>
  );
}

function FeatureItem({
  label,
  title,
  body,
}: {
  label: string;
  title: string;
  body: string;
}) {
  return (
    <div>
      <span className="text-[11px] font-medium tracking-[0.15em] text-[var(--color-terra)] uppercase mb-3 block font-[family-name:var(--font-mono)]">
        {label}
      </span>
      <h3 className="text-xl md:text-2xl tracking-tight text-fd-foreground mb-3">
        {title}
      </h3>
      <p className="text-fd-muted-foreground text-sm leading-relaxed">{body}</p>
    </div>
  );
}

function FooterCTA() {
  return (
    <section className="px-6 pt-16 pb-24 md:px-12 lg:px-20 max-w-7xl mx-auto">
      <div className="border-t border-fd-border pt-16 flex flex-col gap-8 sm:flex-row sm:items-end sm:justify-between">
        <div>
          <h2 className="text-2xl md:text-3xl tracking-tight text-fd-foreground mb-3">
            Get started in seconds
          </h2>
          <p className="text-fd-muted-foreground text-sm">
            One binary, one config file. No containers required.
          </p>
        </div>
        <code className="text-[13px] font-[family-name:var(--font-mono)] bg-fd-muted px-4 py-2.5 rounded-md text-fd-foreground whitespace-nowrap shrink-0">
          go install github.com/vaayne/anna@latest
        </code>
      </div>
    </section>
  );
}
