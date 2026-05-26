import React from 'react';
import ReactDOM from 'react-dom/client';
import './styles.css';

type HealthResponse = {
  ok: boolean;
};

type Repo = {
  id: string;
  name: string;
  git_repo_path: string;
  default_target_branch: string;
};

type Task = {
  id: string;
  repo_id: string;
  title: string;
  description: string;
  status: string;
};

type BranchInfo = {
  name: string;
  kind: 'local' | 'remote' | 'workspace';
  current: boolean;
  workspace_count: number;
};

type RepoStructure = {
  repo: Repo;
  branches: BranchInfo[];
};

type StartWorkspaceResponse = {
  workspace_id: string;
  session_id: string;
  execution_process_id: string;
  worktree_path: string;
  branch: string;
};

type ExecutorInfo = {
  id: string;
  name: string;
  available: boolean;
  detail: string;
};

type Session = {
  id: string;
  workspace_id: string;
  name: string;
  executor_id: string;
  agent_working_dir: string;
  created_at: string;
};

type ExecutionProcess = {
  id: string;
  session_id: string;
  workspace_id: string;
  run_reason: string;
  executor_id: string;
  status: string;
  exit_code: number | null;
  pid: number | null;
  started_at: string;
  finished_at: string;
};

type WorkspaceSummary = {
  id: string;
  task_id: string;
  repo_id: string;
  branch: string;
  base_branch: string;
  container_ref: string;
  worktree_deleted: boolean;
  created_at: string;
  repo: Repo;
  task: Task;
  session?: Session;
  latest_execution?: ExecutionProcess;
};

type WorkspaceDetail = {
  workspace: WorkspaceSummary;
  sessions: Session[];
  executions: ExecutionProcess[];
};

type WorkspaceDiff = {
  workspace_id: string;
  base_branch: string;
  base_commit: string;
  head_commit: string;
  changed: boolean;
  stat: string;
  patch: string;
};

type ExecutionLogsResponse = {
  execution_process_id: string;
  logs: Array<{
    seq: number;
    kind: 'ready' | 'stdout' | 'stderr' | 'finished';
    data: string;
    created_at: string;
  }>;
};

type LogLine = {
  id: number;
  kind: 'ready' | 'stdout' | 'stderr' | 'finished' | 'system';
  text: string;
};

const apiBase = import.meta.env.VITE_API_BASE ?? 'http://localhost:8080';
const branchFoldLimit = 8;

// App 是真实 M1 操作台：登记本地仓库、创建任务、启动真实 worktree Echo，并展示 SSE 日志。
function App() {
  const [health, setHealth] = React.useState<'loading' | 'ok' | 'error'>('loading');
  const [healthDetail, setHealthDetail] = React.useState('Checking Go server health...');
  const [repos, setRepos] = React.useState<Repo[]>([]);
  const [repoStructures, setRepoStructures] = React.useState<RepoStructure[]>([]);
  const [tasks, setTasks] = React.useState<Task[]>([]);
  const [workspaces, setWorkspaces] = React.useState<WorkspaceSummary[]>([]);
  const [selectedWorkspace, setSelectedWorkspace] = React.useState<WorkspaceDetail | null>(null);
  const [workspaceDiff, setWorkspaceDiff] = React.useState<WorkspaceDiff | null>(null);
  const [reviewStatus, setReviewStatus] = React.useState('Select a workspace to inspect logs and diff.');
  const [repoForm, setRepoForm] = React.useState({ name: '', git_repo_path: '', default_target_branch: 'main' });
  const [taskForm, setTaskForm] = React.useState({ repo_id: '', title: '', description: '' });
  const [executors, setExecutors] = React.useState<ExecutorInfo[]>([]);
  const [executionForm, setExecutionForm] = React.useState({ executor_id: 'ECHO', prompt: '' });
  const [expandedRepos, setExpandedRepos] = React.useState<Record<string, boolean>>({});
  const [executionID, setExecutionID] = React.useState('');
  const [worktreePath, setWorktreePath] = React.useState('');
  const [branch, setBranch] = React.useState('');
  const [isRunning, setIsRunning] = React.useState(false);
  const [logs, setLogs] = React.useState<LogLine[]>([]);
  const eventSourceRef = React.useRef<EventSource | null>(null);
  const nextLogID = React.useRef(1);

  // appendLog 追加一条前端日志，用递增 id 保证 React 渲染列表稳定。
  const appendLog = React.useCallback((kind: LogLine['kind'], text: string) => {
    setLogs((current) => [...current, { id: nextLogID.current++, kind, text }]);
  }, []);

  // useEffect 在页面加载时完成健康检查和初始列表加载。
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
          setHealth('ok');
          setHealthDetail('Go server is reachable.');
        }
      } catch (error) {
        if (!cancelled) {
          setHealth('error');
          setHealthDetail(error instanceof Error ? error.message : 'Unknown error');
        }
      }
    }

    void checkHealth();
    void loadRepos();
    void loadRepoStructure();
    void loadTasks();
    void loadWorkspaces();
    void loadExecutors();
    return () => {
      cancelled = true;
      eventSourceRef.current?.close();
    };
  }, []);

  // loadRepos 从后端读取真实 repo 列表，并在没有选中 repo 时默认选择第一项。
  async function loadRepos() {
    const response = await fetch(`${apiBase}/api/repos`);
    if (!response.ok) {
      throw new Error(`load repos failed: HTTP ${response.status}`);
    }
    const data = (await response.json()) as Repo[];
    setRepos(data);
    setTaskForm((current) => ({ ...current, repo_id: current.repo_id || data[0]?.id || '' }));
  }

  // loadRepoStructure 读取仓库和分支结构，用于绘制可折叠的 repository graph。
  async function loadRepoStructure() {
    const response = await fetch(`${apiBase}/api/repos/structure`);
    if (!response.ok) {
      throw new Error(`load repo structure failed: HTTP ${response.status}`);
    }
    setRepoStructures((await response.json()) as RepoStructure[]);
  }

  // loadTasks 读取 task 列表；M1 允许空 repo_id 时返回全部任务，方便本地调试。
  async function loadTasks(repoID = '') {
    const query = repoID ? `?repo_id=${encodeURIComponent(repoID)}` : '';
    const response = await fetch(`${apiBase}/api/tasks${query}`);
    if (!response.ok) {
      throw new Error(`load tasks failed: HTTP ${response.status}`);
    }
    setTasks((await response.json()) as Task[]);
  }

  // loadWorkspaces 读取 M1.5 审查列表，展示已创建 worktree、最新执行状态和关联 task。
  async function loadWorkspaces() {
    const response = await fetch(`${apiBase}/api/workspaces`);
    if (!response.ok) {
      throw new Error(`load workspaces failed: HTTP ${response.status}`);
    }
    setWorkspaces((await response.json()) as WorkspaceSummary[]);
  }

  // loadExecutors 读取后端注册的执行器列表；M2a 只有 Echo，但 API 形状为 M2b 接 Claude 预留。
  async function loadExecutors() {
    const response = await fetch(`${apiBase}/api/executors`);
    if (!response.ok) {
      throw new Error(`load executors failed: HTTP ${response.status}`);
    }
    const data = (await response.json()) as ExecutorInfo[];
    setExecutors(data);
    setExecutionForm((current) => ({ ...current, executor_id: current.executor_id || data[0]?.id || 'ECHO' }));
  }

  // loadWorkspaceReview 读取单个 workspace 的详情和 diff，并默认打开它的最新 execution 日志。
  async function loadWorkspaceReview(workspaceID: string, preferredExecutionID = '') {
    setReviewStatus('Loading workspace review...');
    eventSourceRef.current?.close();
    try {
      const [detailResponse, diffResponse] = await Promise.all([
        fetch(`${apiBase}/api/workspaces/${workspaceID}`),
        fetch(`${apiBase}/api/workspaces/${workspaceID}/diff`),
      ]);
      if (!detailResponse.ok) {
        const err = await detailResponse.json();
        throw new Error(err.error ?? `HTTP ${detailResponse.status}`);
      }
      if (!diffResponse.ok) {
        const err = await diffResponse.json();
        throw new Error(err.error ?? `HTTP ${diffResponse.status}`);
      }
      const detail = (await detailResponse.json()) as WorkspaceDetail;
      const diff = (await diffResponse.json()) as WorkspaceDiff;
      setSelectedWorkspace(detail);
      setWorkspaceDiff(diff);
      setWorktreePath(detail.workspace.container_ref);
      setBranch(detail.workspace.branch);
      setReviewStatus(diff.changed ? 'Workspace has uncommitted review diff.' : 'Workspace has no diff against base.');

      const execution =
        detail.executions.find((item) => item.id === preferredExecutionID) ?? detail.executions[0] ?? null;
      if (execution) {
        openExecution(execution);
      } else {
        setExecutionID('');
        setLogs([]);
        setIsRunning(false);
      }
      await loadWorkspaces();
    } catch (error) {
      setReviewStatus(error instanceof Error ? error.message : 'failed to load workspace review');
    }
  }

  // openExecution 打开指定执行记录：已结束的执行从 DB 读取历史日志，运行中的执行连接 SSE 继续实时追加。
  function openExecution(execution: ExecutionProcess) {
    setExecutionID(execution.id);
    setIsRunning(execution.status === 'running');
    setLogs([]);
    nextLogID.current = 1;
    if (execution.status === 'running') {
      connectEvents(execution.id);
      return;
    }
    void loadExecutionLogs(execution.id);
  }

  // loadExecutionLogs 从后端历史日志接口回放指定 execution 的完整日志。
  async function loadExecutionLogs(id: string) {
    try {
      const response = await fetch(`${apiBase}/api/execution-processes/${id}/logs`);
      if (!response.ok) {
        const err = await response.json();
        throw new Error(err.error ?? `HTTP ${response.status}`);
      }
      const data = (await response.json()) as ExecutionLogsResponse;
      nextLogID.current = 1;
      setLogs(
        data.logs.map((line) => ({
          id: nextLogID.current++,
          kind: line.kind,
          text: line.data || line.kind,
        })),
      );
    } catch (error) {
      appendLog('stderr', error instanceof Error ? error.message : 'failed to load execution logs');
    }
  }

  // createRepo 提交本地 git 仓库登记表单；后端会校验路径是否为 git repo。
  async function createRepo(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    appendLog('system', 'registering repo...');
    try {
      const response = await fetch(`${apiBase}/api/repos`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(repoForm),
      });
      if (!response.ok) {
        const err = await response.json();
        throw new Error(err.error ?? `HTTP ${response.status}`);
      }
      const repo = (await response.json()) as Repo;
      setRepoForm({ name: '', git_repo_path: '', default_target_branch: 'main' });
      setTaskForm((current) => ({ ...current, repo_id: repo.id }));
      await loadRepos();
      await loadRepoStructure();
      await loadTasks(repo.id);
      await loadWorkspaces();
      appendLog('system', `repo registered: ${repo.name}`);
    } catch (error) {
      appendLog('stderr', error instanceof Error ? error.message : 'failed to register repo');
    }
  }

  // createTask 在选中的真实 repo 下创建 task，后续 Start Echo 会基于它创建 worktree。
  async function createTask(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    appendLog('system', 'creating task...');
    try {
      const response = await fetch(`${apiBase}/api/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(taskForm),
      });
      if (!response.ok) {
        const err = await response.json();
        throw new Error(err.error ?? `HTTP ${response.status}`);
      }
      const task = (await response.json()) as Task;
      setTaskForm((current) => ({ ...current, title: '', description: '' }));
      await loadTasks(task.repo_id);
      await loadWorkspaces();
      appendLog('system', `task created: ${task.title}`);
    } catch (error) {
      appendLog('stderr', error instanceof Error ? error.message : 'failed to create task');
    }
  }

  // startTask 调用真实 M2a 启动接口：后端会创建 git worktree、落库 session/process，并交给选中的 executor 运行。
  async function startTask(taskID: string) {
    setLogs([]);
    nextLogID.current = 1;
    appendLog('system', 'starting real worktree echo flow...');
    setIsRunning(true);
    setWorktreePath('');
    setBranch('');

    try {
      const response = await fetch(`${apiBase}/api/tasks/${taskID}/workspaces`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(executionForm),
      });
      if (!response.ok) {
        const err = await response.json();
        throw new Error(err.error ?? `HTTP ${response.status}`);
      }
      const data = (await response.json()) as StartWorkspaceResponse;
      setExecutionID(data.execution_process_id);
      setWorktreePath(data.worktree_path);
      setBranch(data.branch);
      appendLog('system', `workspace ${data.workspace_id}`);
      appendLog('system', `worktree ${data.worktree_path}`);
      await loadRepoStructure();
      await loadWorkspaces();
      await loadWorkspaceReview(data.workspace_id, data.execution_process_id);
    } catch (error) {
      setIsRunning(false);
      appendLog('stderr', error instanceof Error ? error.message : 'failed to start task');
    }
  }

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
      void loadWorkspaces();
    });
    source.onerror = () => {
      appendLog('system', 'event stream disconnected');
      setIsRunning(false);
      source.close();
    };
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
        const err = await response.json();
        throw new Error(err.error ?? `HTTP ${response.status}`);
      }
    } catch (error) {
      appendLog('stderr', error instanceof Error ? error.message : 'failed to stop process');
      setIsRunning(false);
    }
  }

  // toggleRepoBranches 切换某个仓库分支列表的折叠状态，避免分支过多时占满页面。
  function toggleRepoBranches(repoID: string) {
    setExpandedRepos((current) => ({ ...current, [repoID]: !current[repoID] }));
  }

  return (
    <main className="shell">
      <section className="workspace" aria-labelledby="app-title">
        <header className="masthead">
          <div>
            <p className="eyebrow">M1 Real Chain</p>
            <h1 id="app-title">Go Vibe</h1>
          </div>
          <div className="top-row">
            <div className={`status status-${health}`}>
              <span className="dot" aria-hidden="true" />
              <span>{health === 'loading' ? 'Checking' : health === 'ok' ? 'Healthy' : 'Unavailable'}</span>
            </div>
            <div className={`status ${isRunning ? 'status-running' : 'status-idle'}`}>
              <span className="dot" aria-hidden="true" />
              <span>{isRunning ? 'Running' : 'Idle'}</span>
            </div>
          </div>
        </header>

        <div className="grid">
          <section className="panel">
            <h2>Repository</h2>
            <p className="detail">{healthDetail}</p>
            <form className="form" onSubmit={createRepo}>
              <label>
                <span>Name</span>
                <input
                  value={repoForm.name}
                  onChange={(event) => setRepoForm((current) => ({ ...current, name: event.target.value }))}
                  placeholder="go-vibe"
                />
              </label>
              <label>
                <span>Git path</span>
                <input
                  value={repoForm.git_repo_path}
                  onChange={(event) => setRepoForm((current) => ({ ...current, git_repo_path: event.target.value }))}
                  placeholder="/home/zhang/harness/go-vibe"
                  required
                />
              </label>
              <label>
                <span>Base branch</span>
                <input
                  value={repoForm.default_target_branch}
                  onChange={(event) =>
                    setRepoForm((current) => ({ ...current, default_target_branch: event.target.value }))
                  }
                  placeholder="main"
                />
              </label>
              <button type="submit">Register Repo</button>
            </form>
          </section>

          <section className="panel">
            <h2>Task</h2>
            <form className="form" onSubmit={createTask}>
              <label>
                <span>Repo</span>
                <select
                  value={taskForm.repo_id}
                  onChange={(event) => {
                    setTaskForm((current) => ({ ...current, repo_id: event.target.value }));
                    void loadTasks(event.target.value);
                  }}
                  required
                >
                  <option value="">Select repo</option>
                  {repos.map((repo) => (
                    <option value={repo.id} key={repo.id}>
                      {repo.name}
                    </option>
                  ))}
                </select>
              </label>
              <label>
                <span>Title</span>
                <input
                  value={taskForm.title}
                  onChange={(event) => setTaskForm((current) => ({ ...current, title: event.target.value }))}
                  placeholder="Run real worktree echo"
                  required
                />
              </label>
              <label>
                <span>Description</span>
                <textarea
                  value={taskForm.description}
                  onChange={(event) => setTaskForm((current) => ({ ...current, description: event.target.value }))}
                  rows={3}
                />
              </label>
              <button type="submit" disabled={!taskForm.repo_id}>
                Create Task
              </button>
            </form>
          </section>
        </div>

        <section className="panel">
          <div className="section-head">
            <h2>Repository Structure</h2>
            <button type="button" className="secondary" onClick={() => void loadRepoStructure()}>
              Refresh
            </button>
          </div>
          <div className="repo-graph">
            {repoStructures.length === 0 ? (
              <p className="empty light">No repositories registered.</p>
            ) : (
              repoStructures.map((item) => {
                const expanded = expandedRepos[item.repo.id] ?? item.branches.length <= branchFoldLimit;
                const visibleBranches = expanded ? item.branches : item.branches.slice(0, branchFoldLimit);
                const hiddenCount = item.branches.length - visibleBranches.length;

                return (
                  <article className="repo-node" key={item.repo.id}>
                    <div className="repo-node-head">
                      <div>
                        <strong>{item.repo.name}</strong>
                        <code>{item.repo.git_repo_path}</code>
                      </div>
                      <span>{item.branches.length} branches</span>
                    </div>
                    <div className="branch-tree">
                      {visibleBranches.length === 0 ? (
                        <p className="empty light">No branches found.</p>
                      ) : (
                        visibleBranches.map((branchItem) => (
                          <div className="branch-node" key={`${branchItem.kind}:${branchItem.name}`}>
                            <span className={`branch-kind branch-${branchItem.kind}`}>{branchItem.kind}</span>
                            <strong>{branchItem.name}</strong>
                            {branchItem.current ? <span className="branch-pill">current</span> : null}
                            {branchItem.workspace_count > 0 ? (
                              <span className="branch-pill">{branchItem.workspace_count} workspaces</span>
                            ) : null}
                          </div>
                        ))
                      )}
                    </div>
                    {item.branches.length > branchFoldLimit ? (
                      <button type="button" className="link-button" onClick={() => toggleRepoBranches(item.repo.id)}>
                        {expanded ? 'Collapse branches' : `Show ${hiddenCount} more branches`}
                      </button>
                    ) : null}
                  </article>
                );
              })
            )}
          </div>
        </section>

        <section className="panel">
          <div className="section-head">
            <h2>Tasks</h2>
            <button type="button" className="secondary" onClick={() => void loadTasks(taskForm.repo_id)}>
              Refresh
            </button>
          </div>
          <div className="executor-controls">
            <label>
              <span>Executor</span>
              <select
                value={executionForm.executor_id}
                onChange={(event) =>
                  setExecutionForm((current) => ({ ...current, executor_id: event.target.value }))
                }
              >
                {executors.length === 0 ? <option value="ECHO">Echo</option> : null}
                {executors.map((executor) => (
                  <option value={executor.id} key={executor.id} disabled={!executor.available}>
                    {executor.name}
                  </option>
                ))}
              </select>
            </label>
            <label>
              <span>Prompt</span>
              <input
                value={executionForm.prompt}
                onChange={(event) => setExecutionForm((current) => ({ ...current, prompt: event.target.value }))}
                placeholder="Optional prompt for the selected executor"
              />
            </label>
          </div>
          <div className="task-list">
            {tasks.length === 0 ? (
              <p className="empty light">No tasks yet.</p>
            ) : (
              tasks.map((task) => (
                <article className="task-row" key={task.id}>
                  <div>
                    <strong>{task.title}</strong>
                    <p>{task.description || 'No description'}</p>
                  </div>
                  <button type="button" onClick={() => void startTask(task.id)} disabled={isRunning}>
                    Start Echo
                  </button>
                </article>
              ))
            )}
          </div>
        </section>

        <section className="panel review">
          <div className="section-head">
            <h2>Workspace Review</h2>
            <button type="button" className="secondary" onClick={() => void loadWorkspaces()}>
              Refresh
            </button>
          </div>
          <div className="review-grid">
            <div className="workspace-list">
              {workspaces.length === 0 ? (
                <p className="empty light">No workspaces yet.</p>
              ) : (
                workspaces.map((workspace) => (
                  <button
                    type="button"
                    className={`workspace-item ${
                      selectedWorkspace?.workspace.id === workspace.id ? 'workspace-item-active' : ''
                    }`}
                    key={workspace.id}
                    onClick={() => void loadWorkspaceReview(workspace.id)}
                  >
                    <span>
                      <strong>{workspace.task.title}</strong>
                      <small>{workspace.repo.name}</small>
                    </span>
                    <code>{workspace.branch}</code>
                    <em className={`exec-status exec-${workspace.latest_execution?.status ?? 'none'}`}>
                      {workspace.latest_execution?.status ?? 'no execution'}
                    </em>
                  </button>
                ))
              )}
            </div>

            <div className="review-detail">
              <p className="detail">{reviewStatus}</p>
              {selectedWorkspace ? (
                <>
                  <div className="meta-grid compact">
                    <div>
                      <span>Workspace</span>
                      <code>{selectedWorkspace.workspace.id}</code>
                    </div>
                    <div>
                      <span>Base</span>
                      <code>{selectedWorkspace.workspace.base_branch}</code>
                    </div>
                    <div>
                      <span>Path</span>
                      <code>{selectedWorkspace.workspace.container_ref}</code>
                    </div>
                  </div>

                  <div className="execution-list">
                    {selectedWorkspace.executions.map((execution) => (
                      <button
                        type="button"
                        className={`execution-chip ${executionID === execution.id ? 'execution-chip-active' : ''}`}
                        key={execution.id}
                        onClick={() => openExecution(execution)}
                      >
                        <span>{execution.executor_id}</span>
                        <strong>{execution.status}</strong>
                      </button>
                    ))}
                  </div>

                  <div className="diff-box">
                    <div className="diff-head">
                      <strong>Diff</strong>
                      <span>{workspaceDiff?.changed ? 'changed' : 'clean'}</span>
                    </div>
                    <pre>{workspaceDiff?.stat || 'No stat output.'}</pre>
                    <pre>{workspaceDiff?.patch || 'No patch output.'}</pre>
                  </div>
                </>
              ) : null}
            </div>
          </div>
        </section>

        <section className="panel execution">
          <div className="section-head">
            <h2>Execution</h2>
            <button type="button" className="secondary" onClick={stopEcho} disabled={!isRunning || !executionID}>
              Stop
            </button>
          </div>
          <div className="meta-grid">
            <div>
              <span>Execution</span>
              <code>{executionID || 'not started'}</code>
            </div>
            <div>
              <span>Branch</span>
              <code>{branch || 'not started'}</code>
            </div>
            <div>
              <span>Worktree</span>
              <code>{worktreePath || 'not started'}</code>
            </div>
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
      </section>
    </main>
  );
}

ReactDOM.createRoot(document.getElementById('root') as HTMLElement).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
