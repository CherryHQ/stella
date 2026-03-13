import { createFileRoute, Link } from "@tanstack/react-router";
import { HomeLayout } from "fumadocs-ui/layouts/home";
import { baseOptions } from "@/lib/layout.shared";

export const Route = createFileRoute("/")({
  component: Home,
});

function Home() {
  return (
    <HomeLayout {...baseOptions()}>
      <div className="flex flex-col flex-1 justify-center items-center px-4 py-16 text-center gap-6 max-w-3xl mx-auto">
        <h1 className="font-bold text-4xl tracking-tight">anna</h1>
        <p className="text-fd-muted-foreground text-lg leading-relaxed">
          Your AI assistant that never forgets. Self-hosted, single binary,
          lossless context management. Talk from your terminal, Telegram, QQ, or
          Feishu — anna remembers everything.
        </p>
        <div className="flex flex-row gap-3">
          <Link
            to="/docs/$"
            params={{ _splat: "" }}
            className="px-4 py-2 rounded-lg bg-fd-primary text-fd-primary-foreground font-medium text-sm"
          >
            Get Started
          </Link>
          <a
            href="https://github.com/vaayne/anna"
            target="_blank"
            rel="noopener noreferrer"
            className="px-4 py-2 rounded-lg border border-fd-border text-fd-foreground font-medium text-sm"
          >
            GitHub
          </a>
        </div>
        <div className="grid grid-cols-1 sm:grid-cols-3 gap-4 mt-8 text-left w-full">
          <FeatureCard
            title="Lossless Memory"
            description="DAG-based context compression. Conversations grow forever without losing detail."
          />
          <FeatureCard
            title="Multi-Channel"
            description="Terminal, Telegram, QQ, Feishu — all sharing the same session and memory."
          />
          <FeatureCard
            title="Self-Hosted"
            description="Single Go binary + SQLite. Your machine, your API keys, nothing leaves your network."
          />
        </div>
      </div>
    </HomeLayout>
  );
}

function FeatureCard({
  title,
  description,
}: {
  title: string;
  description: string;
}) {
  return (
    <div className="rounded-lg border border-fd-border p-4">
      <h3 className="font-semibold text-sm mb-1">{title}</h3>
      <p className="text-fd-muted-foreground text-sm">{description}</p>
    </div>
  );
}
