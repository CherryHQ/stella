import { useCallback, useMemo, useState } from "react";
import { useNavigate, useParams } from "@tanstack/react-router";
import { useInfiniteQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import type { Session } from "@/lib/types";
import { sessionsInfiniteQueryOptions } from "@/lib/queries/sessions";
import { useI18n } from "@/lib/i18n";

const SUGGESTION_CARDS = [
  {
    title: "Turn this into tasks",
    body: "Break a product idea into linked tasks with owners, status, and next actions.",
    icon: (
      <svg
        width="17"
        height="17"
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.8"
      >
        <path d="m9 11 3 3L22 4" />
        <path d="M21 12v7a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11" />
      </svg>
    ),
  },
  {
    title: "Create automation",
    body: "Set up a repeatable workflow for reviews, daily summaries, or skill updates.",
    icon: (
      <svg
        width="17"
        height="17"
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.8"
      >
        <path d="M13 2 3 14h8l-1 8 11-13h-8l0-7z" />
      </svg>
    ),
  },
  {
    title: "Explore project",
    body: "Read files, summarize architecture, and identify the safest implementation path.",
    icon: (
      <svg
        width="17"
        height="17"
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.8"
      >
        <path d="M4 7h6l2 2h8v9a2 2 0 0 1-2 2H4z" />
      </svg>
    ),
  },
  {
    title: "Manage skills",
    body: "Install, audit, or package skills and channels for project-specific workflows.",
    icon: (
      <svg
        width="17"
        height="17"
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.8"
      >
        <path d="M12 2v20M2 12h20M4.9 4.9l14.2 14.2M19.1 4.9 4.9 19.1" />
      </svg>
    ),
  },
];

export function AgentHome() {
  const { agentId } = useParams({ from: "/_app/agents/$agentId/" });
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { t } = useI18n();
  const [prompt, setPrompt] = useState("");

  const sessionsQuery = useInfiniteQuery(sessionsInfiniteQueryOptions(agentId));
  const sessions = sessionsQuery.data?.pages.flat() ?? [];

  const mainSession = useMemo(() => {
    const active = sessions.filter((s) => !s.archived);
    return active.find((s) => s.kind === "main") ?? active.find((s) => s.kind === "chat") ?? null;
  }, [sessions]);

  const goToSession = useCallback(
    async (message?: string) => {
      let sid = mainSession?.id;
      if (!sid) {
        const sess = await api<Session>(
          "POST",
          `/api/agents/${encodeURIComponent(agentId)}/sessions`,
          {
            kind: "main",
          },
        );
        await queryClient.invalidateQueries({ queryKey: ["sessions", agentId] });
        sid = sess.id;
      }
      void navigate({
        to: "/agents/$agentId/sessions/$sessionId",
        params: { agentId, sessionId: sid },
        search: message?.trim() ? { draft: message.trim() } : undefined,
      });
    },
    [agentId, mainSession, queryClient, navigate],
  );

  if (sessionsQuery.isLoading) {
    return (
      <div className="flex flex-1 items-center justify-center bg-card/70">
        <div className="size-4 animate-spin rounded-full border-2 border-muted-foreground/30 border-t-muted-foreground" />
      </div>
    );
  }

  return (
    <div className="flex flex-1 flex-col overflow-y-auto bg-gradient-to-b from-card/80 to-card">
      <div className="mx-auto grid w-full max-w-5xl flex-1 content-center gap-7 px-4 py-8 sm:px-8">
        <section className="mx-auto max-w-3xl text-center sm:text-left md:text-center">
          <div className="mb-5 grid size-16 place-items-center rounded-[20px] bg-foreground text-2xl font-bold text-background shadow-[0_18px_40px_rgba(29,29,31,0.22)] sm:mx-0 md:mx-auto">
            S
          </div>
          <h1 className="text-4xl font-bold tracking-[-0.055em] text-foreground sm:text-6xl">
            What should Stella work on?
          </h1>
          <p className="mx-auto mt-4 max-w-2xl text-base leading-7 text-muted-foreground sm:text-lg">
            Start a durable main session for planning, building, automation, research, or project
            context work.
          </p>
        </section>

        <section className="mx-auto w-full max-w-3xl overflow-hidden rounded-[24px] border border-border/80 bg-card shadow-[0_22px_54px_rgba(29,29,31,0.10)]">
          <textarea
            value={prompt}
            onChange={(e) => setPrompt(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && !e.shiftKey) {
                e.preventDefault();
                void goToSession(prompt);
              }
            }}
            rows={3}
            placeholder="Ask Stella to plan, build, automate, research, or manage project work…"
            className="min-h-24 w-full resize-none border-0 bg-transparent px-5 pt-5 text-base text-foreground outline-none placeholder:text-muted-foreground"
          />
          <div className="flex items-center justify-between gap-3 px-3 pb-3">
            <div className="flex flex-wrap gap-2">
              {["Attach file", "Use project context", "Automation"].map((label) => (
                <button
                  key={label}
                  type="button"
                  onClick={() => setPrompt(label)}
                  className="h-8 rounded-full bg-muted px-3 text-xs text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground"
                >
                  {label}
                </button>
              ))}
            </div>
            <button
              type="button"
              onClick={() => void goToSession(prompt)}
              className="grid size-9 shrink-0 place-items-center rounded-full bg-primary text-primary-foreground shadow-[0_8px_18px_color-mix(in_oklch,var(--primary)_25%,transparent)] transition-transform active:scale-95"
              aria-label={t("sessions.sidebar.newChat")}
            >
              <svg
                className="size-4"
                viewBox="0 0 24 24"
                fill="none"
                stroke="currentColor"
                strokeWidth="2.2"
              >
                <path d="M22 2 11 13" />
                <path d="m22 2-7 20-4-9-9-4 20-7z" />
              </svg>
            </button>
          </div>
        </section>

        <section className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          {SUGGESTION_CARDS.map(({ title, body, icon }) => (
            <button
              key={title}
              type="button"
              onClick={() => setPrompt(title)}
              className="min-h-36 rounded-[18px] border border-border/80 bg-card/80 p-4 text-left shadow-[0_12px_32px_rgba(29,29,31,0.045)] transition-all hover:-translate-y-0.5 hover:border-primary/35 hover:shadow-[0_18px_42px_rgba(29,29,31,0.08)]"
            >
              <div className="mb-4 grid size-[34px] place-items-center rounded-[11px] bg-muted text-primary">
                {icon}
              </div>
              <h3 className="text-sm font-semibold tracking-[-0.01em]">{title}</h3>
              <p className="mt-2 text-xs leading-5 text-muted-foreground">{body}</p>
            </button>
          ))}
        </section>
      </div>
    </div>
  );
}
