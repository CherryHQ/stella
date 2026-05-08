import { createFileRoute } from '@tanstack/react-router';
import { useMemo, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { createClient } from '@anna/client/client';
import { getMeOptions, listAgentsOptions } from '@anna/client/@tanstack/react-query';
import type { Agent, MeResponse } from '@anna/client';
import { HomeLayout } from 'fumadocs-ui/layouts/home';
import { baseOptions } from '@/lib/layout.shared';

export const Route = createFileRoute('/$lang/api-playground')({
  component: Playground,
  head: () => ({
    meta: [{ title: 'API Playground — anna' }],
  }),
});

function Playground() {
  const { lang } = Route.useParams();
  const [serverUrl, setServerUrl] = useState('http://127.0.0.1:25678');
  const [token, setToken] = useState('');
  const [connected, setConnected] = useState(false);

  return (
    <HomeLayout {...baseOptions(lang)}>
      <div className="max-w-4xl mx-auto px-6 py-16">
        <h1 className="text-3xl md:text-4xl tracking-tight text-fd-foreground mb-3">
          API Playground
        </h1>
        <p className="text-fd-muted-foreground text-base mb-10">
          Connect to a running anna server and explore the API using the generated TypeScript SDK.
        </p>

        <ConnectionForm
          serverUrl={serverUrl}
          token={token}
          connected={connected}
          onServerUrl={setServerUrl}
          onToken={setToken}
          onConnect={() => setConnected(true)}
          onDisconnect={() => setConnected(false)}
        />

        {connected && <ApiDemo serverUrl={serverUrl} token={token} />}
      </div>
    </HomeLayout>
  );
}

function ConnectionForm({
  serverUrl,
  token,
  connected,
  onServerUrl,
  onToken,
  onConnect,
  onDisconnect,
}: {
  serverUrl: string;
  token: string;
  connected: boolean;
  onServerUrl: (v: string) => void;
  onToken: (v: string) => void;
  onConnect: () => void;
  onDisconnect: () => void;
}) {
  return (
    <div className="border border-fd-border rounded-lg p-6 mb-10 bg-fd-card">
      <h2 className="text-sm font-medium text-fd-muted-foreground uppercase tracking-widest mb-4">
        Connection
      </h2>
      <div className="flex flex-col gap-4">
        <label className="flex flex-col gap-1.5">
          <span className="text-sm font-medium text-fd-foreground">Server URL</span>
          <input
            type="url"
            value={serverUrl}
            disabled={connected}
            onChange={(e) => onServerUrl(e.target.value)}
            placeholder="http://127.0.0.1:25678"
            className="border border-fd-border rounded-md px-3 py-2 text-sm bg-fd-background text-fd-foreground placeholder:text-fd-muted-foreground focus:outline-none focus:ring-2 focus:ring-[var(--color-terra)] disabled:opacity-50"
          />
        </label>
        <label className="flex flex-col gap-1.5">
          <span className="text-sm font-medium text-fd-foreground">Bearer Token</span>
          <input
            type="password"
            value={token}
            disabled={connected}
            onChange={(e) => onToken(e.target.value)}
            placeholder="your-api-token"
            className="border border-fd-border rounded-md px-3 py-2 text-sm bg-fd-background text-fd-foreground placeholder:text-fd-muted-foreground focus:outline-none focus:ring-2 focus:ring-[var(--color-terra)] disabled:opacity-50"
          />
        </label>
        <div>
          {connected ? (
            <button
              onClick={onDisconnect}
              className="px-4 py-2 text-sm font-medium rounded-md border border-fd-border text-fd-foreground hover:bg-fd-muted transition-colors"
            >
              Disconnect
            </button>
          ) : (
            <button
              disabled={!serverUrl}
              onClick={onConnect}
              className="px-4 py-2 text-sm font-medium rounded-md bg-[var(--color-terra)] text-white hover:bg-[var(--color-terra-light)] transition-colors disabled:opacity-40"
            >
              Connect
            </button>
          )}
        </div>
      </div>
    </div>
  );
}

function ApiDemo({ serverUrl, token }: { serverUrl: string; token: string }) {
  const client = useMemo(
    () =>
      createClient({
        baseUrl: serverUrl,
        headers: token ? { Authorization: `Bearer ${token}` } : {},
      }),
    [serverUrl, token],
  );

  return (
    <div className="flex flex-col gap-8">
      <MeSection client={client} />
      <AgentsSection client={client} />
    </div>
  );
}

type AnnaClient = ReturnType<typeof createClient>;

function MeSection({ client }: { client: AnnaClient }) {
  const { data, isPending, isError, error } = useQuery(getMeOptions({ client }));

  return (
    <Section title="Current User" subtitle="getMeOptions()">
      <QueryResult isPending={isPending} isError={isError} error={error}>
        {data && <MeCard me={data} />}
      </QueryResult>
    </Section>
  );
}

function MeCard({ me }: { me: MeResponse }) {
  return (
    <div className="flex flex-col gap-1">
      <span className="text-sm font-medium text-fd-foreground">{me.username}</span>
      <span className="text-xs text-fd-muted-foreground">
        Role: {me.role} · ID: {me.id}
      </span>
    </div>
  );
}

function AgentsSection({ client }: { client: AnnaClient }) {
  const { data, isPending, isError, error } = useQuery(listAgentsOptions({ client }));

  return (
    <Section title="Agents" subtitle="listAgentsOptions()">
      <QueryResult isPending={isPending} isError={isError} error={error}>
        {data && (
          <div className="flex flex-col gap-2">
            {data.items.length === 0 ? (
              <p className="text-sm text-fd-muted-foreground">No agents found.</p>
            ) : (
              data.items.map((agent: Agent) => (
                <div
                  key={agent.id}
                  className="border border-fd-border rounded-md px-4 py-3 flex flex-col gap-0.5"
                >
                  <span className="text-sm font-medium text-fd-foreground">{agent.name}</span>
                  <span className="text-xs text-fd-muted-foreground">
                    {agent.model} · {agent.scope}
                  </span>
                </div>
              ))
            )}
          </div>
        )}
      </QueryResult>
    </Section>
  );
}

function Section({
  title,
  subtitle,
  children,
}: {
  title: string;
  subtitle: string;
  children: React.ReactNode;
}) {
  return (
    <div className="border border-fd-border rounded-lg overflow-hidden">
      <div className="border-b border-fd-border px-5 py-3 flex items-center justify-between bg-fd-muted/40">
        <span className="text-sm font-medium text-fd-foreground">{title}</span>
        <code className="text-xs text-fd-muted-foreground font-mono">{subtitle}</code>
      </div>
      <div className="p-5">{children}</div>
    </div>
  );
}

function QueryResult({
  isPending,
  isError,
  error,
  children,
}: {
  isPending: boolean;
  isError: boolean;
  error: unknown;
  children: React.ReactNode;
}) {
  if (isPending) {
    return <p className="text-sm text-fd-muted-foreground animate-pulse">Loading…</p>;
  }
  if (isError) {
    const msg = error instanceof Error ? error.message : String(error);
    return <p className="text-sm text-red-500">Error: {msg}</p>;
  }
  return <>{children}</>;
}
