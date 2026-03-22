import { createFileRoute } from '@tanstack/react-router';

export const Route = createFileRoute('/oauth/callback')({
  component: OAuthCallback,
  head: () => ({
    meta: [{ title: 'Authorization Code - Anna' }],
  }),
});

function OAuthCallback() {
  const code =
    typeof window !== 'undefined'
      ? new URLSearchParams(window.location.search).get('code')
      : null;

  return (
    <div
      style={{
        minHeight: '100vh',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        fontFamily:
          '-apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif',
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
        {code ? <SuccessView code={code} /> : <ErrorView />}
      </div>
    </div>
  );
}

function SuccessView({ code }: { code: string }) {
  const copyCode = () => {
    navigator.clipboard.writeText(`/auth ${code}`);
  };

  return (
    <>
      <div style={{ fontSize: '3rem', marginBottom: '1rem' }}>&#10003;</div>
      <h1 style={{ fontSize: '1.5rem', fontWeight: 600, margin: '0 0 0.5rem' }}>
        Authorization Successful
      </h1>
      <p style={{ color: '#666', margin: '0 0 1.5rem', lineHeight: 1.5 }}>
        Copy the command below and send it to your Anna bot in Feishu:
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
        /auth {code}
      </div>
      <button
        type="button"
        onClick={copyCode}
        style={{
          background: '#3370ff',
          color: '#fff',
          border: 'none',
          borderRadius: '8px',
          padding: '0.75rem 2rem',
          fontSize: '1rem',
          fontWeight: 500,
          cursor: 'pointer',
        }}
      >
        Copy Command
      </button>
      <p
        style={{
          color: '#999',
          fontSize: '0.8rem',
          margin: '1.5rem 0 0',
          lineHeight: 1.5,
        }}
      >
        After sending the command, you can close this page.
      </p>
    </>
  );
}

function ErrorView() {
  return (
    <>
      <div style={{ fontSize: '3rem', marginBottom: '1rem' }}>&#9888;</div>
      <h1 style={{ fontSize: '1.5rem', fontWeight: 600, margin: '0 0 0.5rem' }}>
        No Authorization Code
      </h1>
      <p style={{ color: '#666', margin: 0, lineHeight: 1.5 }}>
        No authorization code was found in the URL. Please go back to your Feishu
        chat and send <code>/auth</code> to start the authorization flow again.
      </p>
    </>
  );
}
