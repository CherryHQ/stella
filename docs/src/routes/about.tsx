import { createFileRoute, Link } from '@tanstack/react-router';
import { HomeLayout } from 'fumadocs-ui/layouts/home';
import { baseOptions } from '@/lib/layout.shared';

export const Route = createFileRoute('/about')({
  component: About,
  head: () => ({
    meta: [{ title: 'About Anna' }],
  }),
});

const traits = [
  {
    label: 'Calm',
    description: 'Quiet intelligence over loud marketing. Anna speaks with clarity, not hype.',
  },
  {
    label: 'Trustworthy',
    description: 'Reliable and consistent. She remembers your context and never loses a detail.',
  },
  {
    label: 'Memory-aware',
    description:
      'Long-term context is a first-class feature, not an afterthought. Every conversation builds on the last.',
  },
  {
    label: 'Local-first',
    description:
      'Your machine, your data. Anna runs as a single binary with SQLite — nothing leaves your network.',
  },
  {
    label: 'Companion',
    description:
      'Not a one-shot tool. Anna is designed for the long run — a digital assistant that grows with you.',
  },
  {
    label: 'Elegant',
    description: 'Minimal and restrained. No clutter, no noise. Just the right amount of presence.',
  },
];

function About() {
  return (
    <HomeLayout {...baseOptions()}>
      <div className="home-page">
        <HeroSection />
        <TraitsSection />
        <VisualIdentity />
        <ClosingSection />
      </div>
    </HomeLayout>
  );
}

function HeroSection() {
  return (
    <section className="px-6 pt-28 pb-24 md:px-12 lg:px-20 max-w-7xl mx-auto">
      <div className="grid grid-cols-1 lg:grid-cols-2 gap-12 lg:gap-20 items-center">
        <div>
          <div className="animate-fade-up stagger-1">
            <p className="text-xs font-medium tracking-[0.2em] uppercase text-[var(--color-terra)] mb-8 font-[family-name:var(--font-mono)]">
              Meet Anna
            </p>
          </div>
          <h1 className="animate-fade-up stagger-2 text-5xl md:text-6xl lg:text-[4.5rem] tracking-tight text-fd-foreground leading-[0.92] mb-10">
            A quiet,
            <br />
            reliable
            <br />
            <span className="italic text-[var(--color-terra)]">companion</span>
          </h1>
          <div className="animate-fade-up stagger-3 max-w-md">
            <p className="text-fd-muted-foreground text-base leading-relaxed">
              Anna is a self-hosted AI assistant with real warmth and digital precision. She
              remembers what you said, connects your workflows across devices, and stays quietly
              useful — day after day.
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

function TraitsSection() {
  return (
    <section className="px-6 pt-20 pb-24 md:px-12 lg:px-20 max-w-7xl mx-auto border-t border-fd-border">
      <h2 className="text-3xl md:text-4xl tracking-tight text-fd-foreground mb-6">
        What defines Anna
      </h2>
      <p className="text-fd-muted-foreground text-base leading-relaxed max-w-2xl mb-20">
        Anna is not another generic AI chatbot. She is a{' '}
        <strong className="text-fd-foreground">calm digital companion</strong> — designed to be
        trustworthy, long-lasting, and deeply aware of your context.
      </p>
      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-x-16 gap-y-14">
        {traits.map((t) => (
          <div key={t.label}>
            <h3 className="text-lg tracking-tight text-fd-foreground mb-2">{t.label}</h3>
            <p className="text-fd-muted-foreground text-sm leading-relaxed">{t.description}</p>
          </div>
        ))}
      </div>
    </section>
  );
}

function VisualIdentity() {
  return (
    <section className="px-6 pt-20 pb-24 md:px-12 lg:px-20 max-w-7xl mx-auto border-t border-fd-border">
      <div className="grid grid-cols-1 lg:grid-cols-2 gap-16 items-start">
        <div>
          <h2 className="text-3xl md:text-4xl tracking-tight text-fd-foreground mb-6">
            Visual identity
          </h2>
          <p className="text-fd-muted-foreground text-base leading-relaxed mb-10">
            Anna's look is intentional: a semi-realistic portrait that feels human but is clearly a
            brand character. She looks like someone real — but she is a digital assistant, and that
            distinction matters.
          </p>
          <div className="space-y-6">
            <IdentityRow
              title="Palette"
              detail="Deep navy blue with champagne gold accents. Blue carries trust and calm; gold carries warmth and memory."
            />
            <IdentityRow
              title="Expression"
              detail="Composed, gentle, attentive. Never overly cheerful, never cold. The feeling of someone who listens."
            />
            <IdentityRow
              title="Background"
              detail="Subtle memory-network motifs — faint nodes and connection lines that hint at long-term context without overwhelming."
            />
            <IdentityRow
              title="Style"
              detail="70% photographic realism, 30% brand stylization. Natural skin texture, soft portrait lighting, recognizable at small sizes."
            />
          </div>
        </div>

        <div className="flex flex-col items-center gap-6 lg:pt-4">
          <div className="grid grid-cols-3 gap-4">
            <Swatch color="#1a2744" label="Navy" />
            <Swatch color="#2a3a5c" label="Mid Blue" />
            <Swatch color="#c4a265" label="Gold" />
          </div>
          <p className="text-fd-muted-foreground text-xs tracking-wide font-[family-name:var(--font-mono)]">
            Deep navy + champagne gold
          </p>
        </div>
      </div>
    </section>
  );
}

function IdentityRow({ title, detail }: { title: string; detail: string }) {
  return (
    <div className="flex gap-4">
      <span className="text-sm font-medium text-fd-foreground w-28 shrink-0">{title}</span>
      <p className="text-fd-muted-foreground text-sm leading-relaxed">{detail}</p>
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
      <span className="text-fd-muted-foreground text-[11px] font-[family-name:var(--font-mono)]">
        {label}
      </span>
    </div>
  );
}

function ClosingSection() {
  return (
    <section className="px-6 pt-16 pb-24 md:px-12 lg:px-20 max-w-7xl mx-auto">
      <div className="border-t border-fd-border pt-16 flex flex-col gap-8 sm:flex-row sm:items-end sm:justify-between">
        <div className="max-w-lg">
          <h2 className="text-2xl md:text-3xl tracking-tight text-fd-foreground mb-3">
            Built to stay
          </h2>
          <p className="text-fd-muted-foreground text-sm leading-relaxed">
            Anna is not about making noise. She earns trust over time — through consistent memory,
            reliable assistance, and quiet presence. The kind of assistant you keep coming back to.
          </p>
        </div>
        <Link
          to="/docs/$"
          params={{ _splat: '' }}
          className="inline-flex items-center gap-2 px-5 py-2.5 bg-[var(--color-terra)] text-white text-sm font-medium rounded-md hover:bg-[var(--color-terra-light)] transition-colors shrink-0"
        >
          Read the docs
          <span aria-hidden="true">&rarr;</span>
        </Link>
      </div>
    </section>
  );
}
