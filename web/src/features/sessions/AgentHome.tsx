import { useCallback, useEffect, useMemo, useState } from "react";
import { useNavigate, useParams } from "@tanstack/react-router";
import { useInfiniteQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import type { Session } from "@/lib/types";
import { sessionsInfiniteQueryOptions } from "@/lib/queries/sessions";
import { useI18n } from "@/lib/i18n";

export function AgentHome() {
  const { agentId } = useParams({ from: "/_app/agents/$agentId/" });
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { t } = useI18n();
  const [prompt, setPrompt] = useState("");

  const sessionsQuery = useInfiniteQuery(sessionsInfiniteQueryOptions(agentId));
  const sessions = sessionsQuery.data?.pages.flat() ?? [];

  const targetSession = useMemo(() => {
    const active = sessions.filter((s) => !s.archived);
    return active.find((s) => s.kind === "main") ?? active.find((s) => s.kind === "chat") ?? null;
  }, [sessions]);

  useEffect(() => {
    if (!sessionsQuery.isLoading && targetSession) {
      void navigate({
        to: "/agents/$agentId/sessions/$sessionId",
        params: { agentId, sessionId: targetSession.id },
        replace: true,
      });
    }
  }, [targetSession, sessionsQuery.isLoading, agentId, navigate]);

  const createSession = useCallback(
    async (message?: string) => {
      const sess = await api<Session>("POST", "/api/sessions", { agent_id: agentId });
      await queryClient.invalidateQueries({ queryKey: ["sessions", agentId] });
      void navigate({
        to: "/agents/$agentId/sessions/$sessionId",
        params: { agentId, sessionId: sess.id },
      });
      if (message?.trim()) {
        // The session page owns streaming; start by opening the durable session and let the user send there.
        setPrompt(message);
      }
    },
    [agentId, queryClient, navigate],
  );

  if (sessionsQuery.isLoading || targetSession) {
    return (
      <div className="flex flex-1 items-center justify-center bg-card/70">
        <div className="size-4 animate-spin rounded-full border-2 border-muted-foreground/30 border-t-muted-foreground" />
      </div>
    );
  }

  return (
    <div className="flex flex-1 overflow-y-auto bg-gradient-to-b from-card/80 to-card px-4 py-8 sm:px-8">
      <div className="mx-auto grid min-h-full w-full max-w-5xl content-center gap-7">
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
            rows={3}
            placeholder="Ask Stella to plan, build, automate, research, or manage project work…"
            className="min-h-24 w-full resize-none border-0 bg-transparent px-5 pt-5 text-base text-foreground outline-none placeholder:text-muted-foreground"
          />
          <div className="flex items-center justify-between gap-3 px-3 pb-3">
            <div className="flex flex-wrap gap-2">
              {["Use project context", "Create automation", "Review code"].map((label) => (
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
              onClick={() => void createSession(prompt)}
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
          {[
            ["Turn this into tasks", "Break a product idea into linked tasks and next actions."],
            ["Create automation", "Set up repeatable reviews, summaries, or checks."],
            ["Explore project", "Read files and identify the safest implementation path."],
            ["Manage skills", "Install, audit, or package project-specific skills."],
          ].map(([title, body]) => (
            <button
              key={title}
              type="button"
              onClick={() => setPrompt(title)}
              className="min-h-36 rounded-[18px] border border-border/80 bg-card/80 p-4 text-left shadow-[0_12px_32px_rgba(29,29,31,0.045)] transition-all hover:-translate-y-0.5 hover:border-primary/35 hover:shadow-[0_18px_42px_rgba(29,29,31,0.08)]"
            >
              <h3 className="text-sm font-semibold tracking-[-0.01em]">{title}</h3>
              <p className="mt-2 text-xs leading-5 text-muted-foreground">{body}</p>
            </button>
          ))}
        </section>
      </div>
    </div>
  );
}
