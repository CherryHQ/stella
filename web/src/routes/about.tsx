import { createFileRoute, Link } from "@tanstack/react-router";
import { t } from "@/lib/docs/translations";
import { i18n } from "@/lib/docs/i18n";
import { SiteHeader } from "@/components/SiteHeader";

export const Route = createFileRoute("/about")({ component: About });

function About() {
  const lang = i18n.defaultLanguage;

  return (
    <>
      <SiteHeader variant="landing" />
      <main className="home-page">
        <HeroSection lang={lang} />
        <TraitsSection lang={lang} />
        <VisualIdentity lang={lang} />
        <ClosingSection lang={lang} />
      </main>
    </>
  );
}

function HeroSection({ lang }: { lang: string }) {
  const tr = t(lang);
  return (
    <section className="px-6 pt-28 pb-24 md:px-12 lg:px-20 max-w-7xl mx-auto">
      <div className="grid grid-cols-1 lg:grid-cols-2 gap-12 lg:gap-20 items-center">
        <div>
          <div className="animate-fade-up stagger-1">
            <p className="text-xs font-medium tracking-[0.2em] uppercase text-[var(--color-terra)] mb-8 font-[family-name:var(--font-mono)]">
              {tr.meetAnna}
            </p>
          </div>
          <h1 className="animate-fade-up stagger-2 text-5xl md:text-6xl lg:text-[4.5rem] tracking-tight text-foreground leading-[0.92] mb-10">
            {tr.aboutHeroTitle1}
            <br />
            {tr.aboutHeroTitle2}
            <br />
            <span className="italic text-[var(--color-terra)]">{tr.aboutHeroTitle3}</span>
          </h1>
          <div className="animate-fade-up stagger-3 max-w-md">
            <p className="text-muted-foreground text-base leading-relaxed">
              {tr.aboutHeroDescription}
            </p>
          </div>
        </div>
        <div className="animate-fade-up stagger-4 flex justify-center lg:justify-end">
          <div className="relative w-72 h-72 md:w-80 md:h-80 lg:w-96 lg:h-96">
            <img
              src="/avatar.png"
              alt="Anna — AI assistant avatar"
              className="w-full h-full rounded-2xl object-cover ring-1 ring-white/[0.08]"
            />
            <div className="absolute -inset-px rounded-2xl ring-1 ring-inset ring-white/[0.06]" />
          </div>
        </div>
      </div>
    </section>
  );
}

function TraitsSection({ lang }: { lang: string }) {
  const tr = t(lang);
  const traits = [
    { label: tr.traitCalm, description: tr.traitCalmDesc },
    { label: tr.traitTrustworthy, description: tr.traitTrustworthyDesc },
    { label: tr.traitMemoryAware, description: tr.traitMemoryAwareDesc },
    { label: tr.traitLocalFirst, description: tr.traitLocalFirstDesc },
    { label: tr.traitCompanion, description: tr.traitCompanionDesc },
    { label: tr.traitElegant, description: tr.traitElegantDesc },
  ];

  return (
    <section className="px-6 pt-20 pb-24 md:px-12 lg:px-20 max-w-7xl mx-auto border-t border-border">
      <h2 className="text-3xl md:text-4xl tracking-tight text-foreground mb-6">{tr.traitsTitle}</h2>
      <p className="text-muted-foreground text-base leading-relaxed max-w-2xl mb-20">
        {tr.traitsIntro} <strong className="text-foreground">{tr.traitsIntroBold}</strong>{" "}
        {tr.traitsIntroEnd}
      </p>
      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-x-16 gap-y-14">
        {traits.map((trait) => (
          <div key={trait.label}>
            <h3 className="text-lg tracking-tight text-foreground mb-2">{trait.label}</h3>
            <p className="text-muted-foreground text-sm leading-relaxed">{trait.description}</p>
          </div>
        ))}
      </div>
    </section>
  );
}

function VisualIdentity({ lang }: { lang: string }) {
  const tr = t(lang);
  return (
    <section className="px-6 pt-20 pb-24 md:px-12 lg:px-20 max-w-7xl mx-auto border-t border-border">
      <div className="grid grid-cols-1 lg:grid-cols-2 gap-16 items-start">
        <div>
          <h2 className="text-3xl md:text-4xl tracking-tight text-foreground mb-6">
            {tr.visualIdentity}
          </h2>
          <p className="text-muted-foreground text-base leading-relaxed mb-10">
            {tr.visualIdentityIntro}
          </p>
          <div className="space-y-6">
            <IdentityRow title={tr.idPalette} detail={tr.idPaletteDetail} />
            <IdentityRow title={tr.idExpression} detail={tr.idExpressionDetail} />
            <IdentityRow title={tr.idBackground} detail={tr.idBackgroundDetail} />
            <IdentityRow title={tr.idStyle} detail={tr.idStyleDetail} />
          </div>
        </div>
        <div className="flex flex-col items-center gap-6 lg:pt-4">
          <div className="grid grid-cols-3 gap-4">
            <Swatch color="#1a2744" label={tr.swatchNavy} />
            <Swatch color="#2a3a5c" label={tr.swatchMidBlue} />
            <Swatch color="#c4a265" label={tr.swatchGold} />
          </div>
          <p className="text-muted-foreground text-xs tracking-wide font-[family-name:var(--font-mono)]">
            {tr.swatchCaption}
          </p>
        </div>
      </div>
    </section>
  );
}

function IdentityRow({ title, detail }: { title: string; detail: string }) {
  return (
    <div className="flex gap-4">
      <span className="text-sm font-medium text-foreground w-28 shrink-0">{title}</span>
      <p className="text-muted-foreground text-sm leading-relaxed">{detail}</p>
    </div>
  );
}

function Swatch({ color, label }: { color: string; label: string }) {
  return (
    <div className="flex flex-col items-center gap-2">
      <div
        className="w-16 h-16 rounded-lg ring-1 ring-white/[0.08]"
        style={{ backgroundColor: color }}
      />
      <span className="text-muted-foreground text-[11px] font-[family-name:var(--font-mono)]">
        {label}
      </span>
    </div>
  );
}

function ClosingSection({ lang }: { lang: string }) {
  const tr = t(lang);
  return (
    <section className="px-6 pt-16 pb-24 md:px-12 lg:px-20 max-w-7xl mx-auto">
      <div className="border-t border-border pt-16 flex flex-col gap-8 sm:flex-row sm:items-end sm:justify-between">
        <div className="max-w-lg">
          <h2 className="text-2xl md:text-3xl tracking-tight text-foreground mb-3">
            {tr.builtToStay}
          </h2>
          <p className="text-muted-foreground text-sm leading-relaxed">{tr.builtToStayBody}</p>
        </div>
        <Link
          to="/docs/$"
          params={{ _splat: "" }}
          className="inline-flex items-center gap-2 px-5 py-2.5 bg-[var(--color-terra)] text-white text-sm font-medium rounded-md hover:bg-[var(--color-terra-light)] transition-colors shrink-0"
        >
          {tr.readTheDocs}
          <span aria-hidden="true">&rarr;</span>
        </Link>
      </div>
    </section>
  );
}
