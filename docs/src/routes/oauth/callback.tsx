import { createFileRoute } from '@tanstack/react-router';

export const Route = createFileRoute('/oauth/callback')({
  component: OAuthCallback,
  head: () => ({
    meta: [{ title: 'Lark CLI Authentication - Anna' }],
  }),
});

function OAuthCallback() {
  return (
    <div
      style={{
        minHeight: '100vh',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        fontFamily: '-apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif',
        background: '#fafafa',
        padding: '2rem',
      }}
    >
      <div
        style={{
          maxWidth: '480px',
          width: '100%',
          background: '#fff',
          borderRadius: '12px',
          boxShadow: '0 2px 12px rgba(0,0,0,0.08)',
          padding: '2.5rem',
          textAlign: 'center',
        }}
      >
        <InfoView />
      </div>
    </div>
  );
}

function InfoView() {
  return (
    <>
      <div style={{ fontSize: '3rem', marginBottom: '1rem' }}>&#9432;</div>
      <h1 style={{ fontSize: '1.5rem', fontWeight: 600, margin: '0 0 0.5rem' }}>
        Feishu OAuth Was Removed
      </h1>
      <p style={{ color: '#666', margin: '0 0 1.5rem', lineHeight: 1.5 }}>
        Anna no longer uses the old Feishu bot OAuth callback flow for workspace tools.
      </p>
      <div
        style={{
          background: '#f5f5f5',
          borderRadius: '8px',
          padding: '1rem',
          fontFamily: 'monospace',
          fontSize: '0.9rem',
          wordBreak: 'break-all',
          marginBottom: '1rem',
        }}
      >
        lark-cli config init --new
        <br />
        lark-cli auth login --recommend
      </div>
      <p
        style={{
          color: '#999',
          fontSize: '0.8rem',
          margin: '1.5rem 0 0',
          lineHeight: 1.5,
        }}
      >
        Add a <code>lark-cli</code> skill yourself if you want Lark workspace actions, and keep
        Feishu configured only as a chat channel.
      </p>
    </>
  );
}
