import React from 'react';
import ReactDOM from 'react-dom/client';
import './styles.css';

type HealthResponse = {
  ok: boolean;
};

type StartResponse = {
  repo_id: string;
  task_id: string;
  workspace_id: string;
  session_id: string;
  execution_process_id: string;
};

type LogLine = {
  id: number;
  kind: 'ready' | 'stdout' | 'stderr' | 'finished' | 'system';
  text: string;
};

const apiBase = import.meta.env.VITE_API_BASE ?? 'http://localhost:8080';

// App 是 M1a 的最小操作台：检查后端健康状态，启动 mock echo 流程，并展示 SSE 日志。
function App() {
  const [status, setStatus] = React.useState<'loading' | 'ok' | 'error'>('loading');
  const [detail, setDetail] = React.useState('Checking Go server health...');
  const [executionID, setExecutionID] = React.useState('');
  const [isRunning, setIsRunning] = React.useState(false);
  const [logs, setLogs] = React.useState<LogLine[]>([]);
  const eventSourceRef = React.useRef<EventSource | null>(null);
  const nextLogID = React.useRef(1);

  // appendLog 追加一条前端日志，用递增 id 保证 React 渲染列表稳定。
  const appendLog = React.useCallback((kind: LogLine['kind'], text: string) => {
    setLogs((current) => [
      ...current,
      {
        id: nextLogID.current++,
        kind,
        text,
      },
    ]);
  }, []);

  // useEffect 在页面加载时做一次健康检查，确认浏览器能访问 Go server。
  React.useEffect(() => {
    let cancelled = false;

    // checkHealth 调用 /api/health，并把结果映射成顶部健康状态。
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
      eventSourceRef.current?.close();
    };
  }, []);

  // connectEvents 连接指定 execution_process 的 SSE 流，并把 ready/stdout/stderr/finished 映射到日志面板。
  function connectEvents(id: string) {
    eventSourceRef.current?.close();
    const source = new EventSource(`${apiBase}/api/events/execution-processes/${id}`);
    eventSourceRef.current = source;

    source.addEventListener('ready', () => {
      appendLog('ready', 'process is ready');
    });
    source.addEventListener('stdout', (event) => {
      appendLog('stdout', event.data);
    });
    source.addEventListener('stderr', (event) => {
      appendLog('stderr', event.data);
    });
    source.addEventListener('finished', () => {
      appendLog('finished', 'process finished');
      setIsRunning(false);
      source.close();
    });
    source.onerror = () => {
      appendLog('system', 'event stream disconnected');
      setIsRunning(false);
      source.close();
    };
  }

  // startEcho 调用后端 mock start 接口，拿到 execution id 后立即订阅它的 SSE 日志流。
  async function startEcho() {
    setLogs([]);
    nextLogID.current = 1;
    appendLog('system', 'starting mock echo flow...');
    setIsRunning(true);

    try {
      const response = await fetch(`${apiBase}/api/mock/start`, { method: 'POST' });
      if (!response.ok) {
        throw new Error(`HTTP ${response.status}`);
      }
      const data = (await response.json()) as StartResponse;
      setExecutionID(data.execution_process_id);
      appendLog('system', `execution ${data.execution_process_id}`);
      connectEvents(data.execution_process_id);
    } catch (error) {
      setIsRunning(false);
      appendLog('stderr', error instanceof Error ? error.message : 'failed to start echo');
    }
  }

  // stopEcho 请求后端停止当前执行进程；最终 finished 事件仍由 SSE 流负责关闭运行态。
  async function stopEcho() {
    if (!executionID) {
      return;
    }
    appendLog('system', 'stopping process...');
    try {
      const response = await fetch(`${apiBase}/api/execution-processes/${executionID}/stop`, {
        method: 'POST',
      });
      if (!response.ok) {
        throw new Error(`HTTP ${response.status}`);
      }
    } catch (error) {
      appendLog('stderr', error instanceof Error ? error.message : 'failed to stop process');
      setIsRunning(false);
    }
  }

  return (
    <main className="shell">
      <section className="panel" aria-labelledby="app-title">
        <p className="eyebrow">M1a Harness Flow</p>
        <h1 id="app-title">Go Vibe</h1>

        <div className="top-row">
          <div className={`status status-${status}`}>
            <span className="dot" aria-hidden="true" />
            <span>{status === 'loading' ? 'Checking health' : status === 'ok' ? 'Healthy' : 'Unavailable'}</span>
          </div>
          <div className={`status ${isRunning ? 'status-running' : 'status-idle'}`}>
            <span className="dot" aria-hidden="true" />
            <span>{isRunning ? 'Running' : 'Idle'}</span>
          </div>
        </div>

        <p className="detail">{detail}</p>
        <code>{apiBase}/api/health</code>

        <div className="actions">
          <button type="button" onClick={startEcho} disabled={isRunning}>
            Start Echo
          </button>
          <button type="button" className="secondary" onClick={stopEcho} disabled={!isRunning || !executionID}>
            Stop
          </button>
        </div>

        <div className="meta">
          <span>Execution</span>
          <code>{executionID || 'not started'}</code>
        </div>

        <section className="logs" aria-label="Execution logs">
          {logs.length === 0 ? (
            <p className="empty">No logs yet.</p>
          ) : (
            logs.map((line) => (
              <div className={`log-line log-${line.kind}`} key={line.id}>
                <span>{line.kind}</span>
                <pre>{line.text}</pre>
              </div>
            ))
          )}
        </section>
      </section>
    </main>
  );
}

ReactDOM.createRoot(document.getElementById('root') as HTMLElement).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
