import { createFileRoute, Link } from "@tanstack/react-router";
import { t } from "@/lib/docs/translations";
import { i18n } from "@/lib/docs/i18n";
import { SiteHeader } from "@/components/SiteHeader";

export const Route = createFileRoute("/")({ component: Home });

const terminalLines = [
  { prompt: true, text: "stella --open" },
  { prompt: false, text: "Admin panel running at http://localhost:8787" },
  { prompt: true, text: "stella" },
  { prompt: false, text: "Daemon started (bots + scheduler)" },
  { prompt: false, text: 'you: "summarize yesterday\'s conversation"' },
  { prompt: false, text: "stella: Yesterday you discussed migrating the" },
  { prompt: false, text: "      auth service to JWT tokens. Key decisions:" },
  { prompt: false, text: "      1. RS256 signing with key rotation ..." },
];

function Home() {
  const lang = i18n.defaultLanguage;

  return (
    <>
      <SiteHeader />
      <main className="home-page">
        <HeroSection lang={lang} />
        <FeaturesSection lang={lang} />
        <MeetStellaSection lang={lang} />
        <FooterCTA lang={lang} />
      </main>
    </>
  );
}

function HeroSection({ lang }: { lang: string }) {
  const tr = t(lang);
  return (
    <section className="px-6 pt-28 pb-24 md:px-12 lg:px-20 max-w-7xl mx-auto">
      <div className="grid grid-cols-1 lg:grid-cols-2 gap-12 lg:gap-16 items-start">
        <div>
          <div className="animate-fade-up stagger-1">
            <p className="text-xs font-medium tracking-[0.2em] uppercase text-[var(--color-terra)] mb-8 font-[family-name:var(--font-mono)]">
              {tr.heroTag}
            </p>
          </div>
          <h1 className="animate-fade-up stagger-2 text-5xl md:text-6xl lg:text-[4.5rem] tracking-tight text-foreground leading-[0.92] mb-10">
            {tr.heroTitle1}
            <br />
            {tr.heroTitle2}
            <br />
            <span className="italic text-[var(--color-terra)]">{tr.heroTitle3}</span>
          </h1>
          <div className="animate-fade-up stagger-3 max-w-md">
            <p className="text-muted-foreground text-base leading-relaxed mb-12">
              {tr.heroDescription}
            </p>
          </div>
          <div className="animate-fade-up stagger-4 flex items-center gap-5">
            <Link
              to="/docs/$"
              params={{ _splat: "" }}
              className="inline-flex items-center gap-2 px-5 py-2.5 bg-[var(--color-terra)] text-white text-sm font-medium rounded-md hover:bg-[var(--color-terra-light)] transition-colors"
            >
              {tr.readTheDocs}
              <span aria-hidden="true">&rarr;</span>
            </Link>
            <a
              href="https://github.com/CherryHQ/stella"
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex items-center gap-2 text-muted-foreground text-sm font-medium underline underline-offset-4 decoration-border hover:text-foreground hover:decoration-foreground transition-colors"
            >
              {tr.sourceOnGithub}
            </a>
          </div>
        </div>
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
                    <span className="text-[var(--color-terra-light)] select-none">$ </span>
                  )}
                  <span className={line.prompt ? "text-white/90" : "text-white/50"}>
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

function FeaturesSection({ lang }: { lang: string }) {
  const tr = t(lang);
  const features = [
    { label: "01", title: tr.feature1Title, body: tr.feature1Body },
    { label: "02", title: tr.feature2Title, body: tr.feature2Body },
    { label: "03", title: tr.feature3Title, body: tr.feature3Body },
    { label: "04", title: tr.feature4Title, body: tr.feature4Body },
  ];

  return (
    <section className="px-6 pt-20 pb-24 md:px-12 lg:px-20 max-w-7xl mx-auto border-t border-border">
      <h2 className="text-3xl md:text-4xl tracking-tight text-foreground mb-20">
        {tr.featuresTitle}
      </h2>
      <div className="grid grid-cols-1 md:grid-cols-2 gap-x-20 gap-y-16">
        {features.map((f) => (
          <FeatureItem key={f.label} {...f} />
        ))}
      </div>
    </section>
  );
}

function FeatureItem({ label, title, body }: { label: string; title: string; body: string }) {
  return (
    <div>
      <span className="text-[11px] font-medium tracking-[0.15em] text-[var(--color-terra)] uppercase mb-3 block font-[family-name:var(--font-mono)]">
        {label}
      </span>
      <h3 className="text-xl md:text-2xl tracking-tight text-foreground mb-3">{title}</h3>
      <p className="text-muted-foreground text-sm leading-relaxed">{body}</p>
    </div>
  );
}

function MeetStellaSection({ lang }: { lang: string }) {
  const tr = t(lang);
  return (
    <section className="px-6 pt-20 pb-24 md:px-12 lg:px-20 max-w-7xl mx-auto border-t border-border">
      <div className="grid grid-cols-1 lg:grid-cols-[1fr_auto] gap-12 lg:gap-20 items-center">
        <div>
          <p className="text-xs font-medium tracking-[0.2em] uppercase text-[var(--color-terra)] mb-6 font-[family-name:var(--font-mono)]">
            {tr.meetStella}
          </p>
          <h2 className="text-3xl md:text-4xl tracking-tight text-foreground mb-6">
            {tr.meetStellaTitle}
          </h2>
          <p className="text-muted-foreground text-base leading-relaxed max-w-lg mb-4">
            {tr.meetStellaBody1}
          </p>
          <p className="text-muted-foreground text-base leading-relaxed max-w-lg mb-8">
            {tr.meetStellaBody2}
          </p>
          <Link
            to="/about"
            className="inline-flex items-center gap-2 text-[var(--color-terra)] text-sm font-medium hover:text-[var(--color-terra-light)] transition-colors"
          >
            {tr.learnMoreAboutStella}
            <span aria-hidden="true">&rarr;</span>
          </Link>
        </div>
        <div className="flex justify-center lg:justify-end">
          <img
            src="/avatar.png"
            alt="Stella — AI assistant"
            className="w-48 h-48 md:w-56 md:h-56 rounded-2xl object-cover ring-1 ring-white/[0.08]"
          />
        </div>
      </div>
    </section>
  );
}

function FooterCTA({ lang }: { lang: string }) {
  const tr = t(lang);
  return (
    <section className="px-6 pt-16 pb-24 md:px-12 lg:px-20 max-w-7xl mx-auto">
      <div className="border-t border-border pt-16 flex flex-col gap-8 sm:flex-row sm:items-end sm:justify-between">
        <div>
          <h2 className="text-2xl md:text-3xl tracking-tight text-foreground mb-3">
            {tr.getStarted}
          </h2>
          <p className="text-muted-foreground text-sm">{tr.getStartedBody}</p>
        </div>
        <code className="text-[13px] font-[family-name:var(--font-mono)] bg-muted px-4 py-2.5 rounded-md text-foreground whitespace-nowrap shrink-0">
          go install github.com/CherryHQ/stella@latest
        </code>
      </div>
    </section>
  );
}
