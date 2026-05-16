import { createFileRoute, Link } from "@tanstack/react-router";
import { t } from "@/lib/docs/translations";
import { SiteHeader } from "@/components/SiteHeader";
import { useI18n } from "@/lib/i18n";
import "./index.css";

export const Route = createFileRoute("/")({ component: Home });

const conversationLines = [
  { role: "user" as const, text: "summarize yesterday's conversation" },
  {
    role: "stella" as const,
    text: "Yesterday you discussed migrating the auth service to JWT tokens. Key decisions: RS256 signing with key rotation, 7-day refresh window. You asked me to remember that staging uses the old session cookies until March.",
  },
  { role: "user" as const, text: "what was the refresh window again?" },
  {
    role: "stella" as const,
    text: "7 days. You chose that over 30 days because the security team flagged long-lived tokens in the Q3 audit.",
  },
];

function Home() {
  const { locale: lang } = useI18n();

  return (
    <div className="relative isolate flex min-h-svh flex-col bg-background text-foreground">
      <SiteHeader />
      <main className="flex-1 home-page">
        <HeroSection lang={lang} />
        <ConversationSection lang={lang} />
        <FeaturesSection lang={lang} />
        <CapabilitiesSection lang={lang} />
        <FooterCTA lang={lang} />
      </main>
    </div>
  );
}

/* ─── Hero ─── */

function HeroSection({ lang }: { lang: string }) {
  const tr = t(lang);
  return (
    <section className="hero-warm-bg relative overflow-hidden">
      {/* Decorative gold radial behind the avatar */}
      <div className="absolute top-1/2 right-[10%] -translate-y-1/2 w-[500px] h-[500px] rounded-full bg-primary/8 blur-[100px] pointer-events-none dark:bg-primary/5" />

      <div className="relative px-6 pt-28 pb-24 md:px-12 lg:px-20 max-w-7xl mx-auto">
        <div className="grid grid-cols-1 lg:grid-cols-[1.1fr_auto] gap-16 lg:gap-24 items-center">
          {/* Text column */}
          <div className="animate-fade-up">
            <p className="text-[11px] font-medium tracking-[0.2em] uppercase text-primary mb-8 font-mono">
              {tr.heroTag}
            </p>
            <h1 className="hero-headline text-foreground mb-8">
              {tr.heroTitle1}
              <br />
              {tr.heroTitle2} <span className="italic text-primary">{tr.heroTitle3}</span>
            </h1>
            <p className="text-muted-foreground text-lg leading-relaxed max-w-[52ch] mb-12">
              {tr.heroDescription}
            </p>
            <div className="flex flex-wrap items-center gap-5">
              <Link
                to="/docs/$"
                params={{ _splat: "" }}
                className="inline-flex items-center gap-2.5 px-7 py-3.5 bg-primary text-primary-foreground text-sm font-semibold rounded-xl hover:bg-primary/90 active:scale-[0.98] transition-all shadow-md"
              >
                {tr.readTheDocs}
                <span aria-hidden="true" className="text-primary-foreground/70">
                  &rarr;
                </span>
              </Link>
              <a
                href="https://github.com/CherryHQ/stella"
                target="_blank"
                rel="noopener noreferrer"
                className="inline-flex items-center gap-2 px-5 py-3.5 text-muted-foreground text-sm font-medium rounded-xl border border-border hover:bg-accent hover:text-foreground transition-colors"
              >
                {tr.sourceOnGithub}
              </a>
            </div>
          </div>

          {/* Avatar column */}
          <div className="animate-fade-up stagger-2 flex justify-center lg:justify-end">
            <div className="relative">
              <div className="absolute -inset-6 rounded-3xl bg-primary/12 blur-3xl dark:bg-primary/8" />
              <div className="absolute -inset-1 rounded-2xl bg-primary/20 dark:bg-primary/10" />
              <img
                src="/avatar.png"
                alt="Stella"
                className="relative w-56 h-56 md:w-68 md:h-68 lg:w-80 lg:h-80 rounded-2xl object-cover"
              />
            </div>
          </div>
        </div>
      </div>
    </section>
  );
}

/* ─── Conversation (dark inverted section) ─── */

function ConversationSection({ lang }: { lang: string }) {
  const tr = t(lang);
  return (
    <section className="conversation-section dark relative overflow-hidden">
      <div className="absolute top-0 left-[20%] w-[400px] h-[400px] rounded-full bg-primary/6 blur-[80px] pointer-events-none" />

      <div className="relative px-6 py-24 md:px-12 lg:px-20 max-w-7xl mx-auto">
        <div className="grid grid-cols-1 lg:grid-cols-[1fr_1.3fr] gap-16 lg:gap-24 items-center">
          <div>
            <p className="text-[11px] font-medium tracking-[0.2em] uppercase text-primary mb-5 font-mono">
              {tr.memoryLabel}
            </p>
            <h2 className="text-3xl md:text-4xl tracking-tight mb-5 leading-tight">
              {tr.memoryTitle}
            </h2>
            <p className="conversation-body-text text-base leading-relaxed max-w-md">
              {tr.memoryBody}
            </p>
          </div>

          {/* Conversation mock */}
          <div className="conversation-window rounded-2xl overflow-hidden shadow-xl">
            <div className="flex items-center gap-2 px-5 py-3 conversation-window-titlebar">
              <div className="w-2.5 h-2.5 rounded-full opacity-30 bg-current" />
              <div className="w-2.5 h-2.5 rounded-full opacity-20 bg-current" />
              <div className="w-2.5 h-2.5 rounded-full opacity-15 bg-current" />
              <span className="ml-3 text-[11px] opacity-40 font-mono tracking-wider uppercase">
                {tr.conversationLabel}
              </span>
            </div>
            <div className="px-5 py-6 space-y-5">
              {conversationLines.map((line, i) => (
                <div
                  key={i}
                  className={`flex ${line.role === "user" ? "justify-end" : "justify-start"}`}
                >
                  <div
                    className={`max-w-[85%] rounded-xl px-4 py-3 text-[13px] leading-relaxed ${
                      line.role === "user"
                        ? "conversation-user-bubble"
                        : "conversation-stella-bubble"
                    }`}
                  >
                    {line.role === "stella" && (
                      <span className="text-primary font-semibold text-[11px] tracking-wide uppercase block mb-1.5">
                        stella
                      </span>
                    )}
                    {line.text}
                  </div>
                </div>
              ))}
            </div>
          </div>
        </div>
      </div>
    </section>
  );
}

/* ─── Features ─── */

function FeaturesSection({ lang }: { lang: string }) {
  const tr = t(lang);
  const features = [
    { label: "01", title: tr.feature1Title, body: tr.feature1Body },
    { label: "02", title: tr.feature2Title, body: tr.feature2Body },
    { label: "03", title: tr.feature3Title, body: tr.feature3Body },
    { label: "04", title: tr.feature4Title, body: tr.feature4Body },
    { label: "05", title: tr.feature5Title, body: tr.feature5Body },
  ];

  return (
    <section className="px-6 py-28 md:px-12 lg:px-20 max-w-7xl mx-auto">
      <p className="text-[11px] font-medium tracking-[0.2em] uppercase text-primary mb-5 font-mono">
        {tr.featuresTitle}
      </p>
      <div className="h-px bg-border mb-20" />

      <div className="space-y-24 md:space-y-32">
        {features.map((f, i) => (
          <div
            key={f.label}
            className="grid grid-cols-1 md:grid-cols-12 gap-6 md:gap-8 items-start"
          >
            {/* Large decorative number */}
            <div
              className={`hidden md:block md:col-span-2 ${i % 2 === 1 ? "md:col-start-11 md:text-right" : ""}`}
            >
              <span className="feature-number font-mono text-primary/10 dark:text-primary/8 select-none leading-none">
                {f.label}
              </span>
            </div>

            {/* Content */}
            <div className={`md:col-span-6 ${i % 2 === 1 ? "md:col-start-3" : "md:col-start-4"}`}>
              <span className="md:hidden text-[11px] font-medium tracking-[0.15em] text-primary uppercase mb-3 block font-mono">
                {f.label}
              </span>
              <h3 className="text-2xl md:text-3xl tracking-tight text-foreground mb-4">
                {f.title}
              </h3>
              <p className="text-muted-foreground text-base leading-relaxed max-w-lg">{f.body}</p>
            </div>
          </div>
        ))}
      </div>
    </section>
  );
}

/* ─── Capabilities ─── */

function CapabilitiesSection({ lang }: { lang: string }) {
  const tr = t(lang);
  const capabilities = [
    { title: tr.capSkills, desc: tr.capSkillsDesc },
    { title: tr.capAgents, desc: tr.capAgentsDesc },
    { title: tr.capReading, desc: tr.capReadingDesc },
    { title: tr.capEmail, desc: tr.capEmailDesc },
    { title: tr.capVault, desc: tr.capVaultDesc },
    { title: tr.capOAuth, desc: tr.capOAuthDesc },
    { title: tr.capPlugins, desc: tr.capPluginsDesc },
    { title: tr.capMCP, desc: tr.capMCPDesc },
    { title: tr.capModels, desc: tr.capModelsDesc },
    { title: tr.capNotifications, desc: tr.capNotificationsDesc },
  ];

  return (
    <section className="capabilities-section">
      <div className="px-6 py-28 md:px-12 lg:px-20 max-w-7xl mx-auto">
        <div className="grid grid-cols-1 lg:grid-cols-[1fr_2fr] gap-12 lg:gap-24">
          {/* Left: sticky heading */}
          <div className="lg:sticky lg:top-24 lg:self-start">
            <p className="text-[11px] font-medium tracking-[0.2em] uppercase text-primary mb-5 font-mono">
              {tr.capabilitiesTitle}
            </p>
            <h2 className="text-3xl md:text-4xl tracking-tight text-foreground leading-tight">
              {tr.capabilitiesSubtitle}
            </h2>
          </div>

          {/* Right: capabilities list */}
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-x-12 gap-y-10">
            {capabilities.map((cap) => (
              <div key={cap.title}>
                <h3 className="font-sans text-base font-semibold text-foreground mb-1.5">
                  {cap.title}
                </h3>
                <p className="text-muted-foreground text-sm leading-relaxed">{cap.desc}</p>
              </div>
            ))}
          </div>
        </div>
      </div>
    </section>
  );
}

/* ─── Footer CTA ─── */

function FooterCTA({ lang }: { lang: string }) {
  const tr = t(lang);
  return (
    <section className="cta-warm-bg">
      <div className="px-6 py-24 md:px-12 lg:px-20 max-w-7xl mx-auto">
        <div className="grid grid-cols-1 md:grid-cols-2 gap-12 md:gap-20 items-end">
          <div>
            <h2 className="text-3xl md:text-4xl tracking-tight text-foreground mb-4">
              {tr.getStarted}
            </h2>
            <p className="text-muted-foreground text-base">{tr.getStartedBody}</p>
          </div>
          <div>
            <div className="space-y-3">
              <div className="cta-code-block rounded-xl px-5 py-4 font-mono text-sm">
                <span className="text-primary select-none">$ </span>
                brew install CherryHQ/tap/stella
              </div>
              <div className="cta-code-block rounded-xl px-5 py-4 font-mono text-sm">
                <span className="text-primary select-none">$ </span>
                stella server
              </div>
            </div>
            <p className="text-muted-foreground text-xs mt-4">{tr.getStartedAlt}</p>
          </div>
        </div>
      </div>
    </section>
  );
}
