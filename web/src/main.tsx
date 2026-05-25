import React from 'react';
import ReactDOM from 'react-dom/client';
import './styles.css';

type HealthResponse = {
  ok: boolean;
};

const apiBase = import.meta.env.VITE_API_BASE ?? 'http://localhost:8080';

function App() {
  const [status, setStatus] = React.useState<'loading' | 'ok' | 'error'>('loading');
  const [detail, setDetail] = React.useState('Checking Go server health...');

  React.useEffect(() => {
    let cancelled = false;

    async function checkHealth() {
      try {
        const response = await fetch(`${apiBase}/api/health`);
        if (!response.ok) {
          throw new Error(`HTTP ${response.status}`);
        }
        const data = (await response.json()) as HealthResponse;
        if (!data.ok) {
          throw new Error('Health response was not ok');
        }
        if (!cancelled) {
          setStatus('ok');
          setDetail('Go server is reachable.');
        }
      } catch (error) {
        if (!cancelled) {
          setStatus('error');
          setDetail(error instanceof Error ? error.message : 'Unknown error');
        }
      }
    }

    void checkHealth();
    return () => {
      cancelled = true;
    };
  }, []);

  return (
    <main className="shell">
      <section className="panel" aria-labelledby="app-title">
        <p className="eyebrow">M0 Harness</p>
        <h1 id="app-title">Go Vibe</h1>
        <div className={`status status-${status}`}>
          <span className="dot" aria-hidden="true" />
          <span>{status === 'loading' ? 'Checking health' : status === 'ok' ? 'Healthy' : 'Unavailable'}</span>
        </div>
        <p className="detail">{detail}</p>
        <code>{apiBase}/api/health</code>
      </section>
    </main>
  );
}

ReactDOM.createRoot(document.getElementById('root') as HTMLElement).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);

