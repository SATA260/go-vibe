CREATE TABLE repos (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    git_repo_path TEXT NOT NULL,
    default_target_branch TEXT NOT NULL DEFAULT 'main',
    setup_script TEXT,
    cleanup_script TEXT,
    dev_script TEXT,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL
);

CREATE TABLE tasks (
    id TEXT PRIMARY KEY,
    repo_id TEXT NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    description TEXT,
    status TEXT NOT NULL,
    parent_task_id TEXT REFERENCES tasks(id),
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL
);

CREATE TABLE workspaces (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    repo_id TEXT NOT NULL REFERENCES repos(id),
    branch TEXT NOT NULL,
    base_branch TEXT NOT NULL,
    container_ref TEXT NOT NULL,
    worktree_deleted BOOLEAN NOT NULL DEFAULT 0,
    created_at TIMESTAMP NOT NULL
);

CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name TEXT,
    executor_id TEXT NOT NULL,
    executor_config JSON NOT NULL,
    agent_session_id TEXT,
    agent_working_dir TEXT,
    created_at TIMESTAMP NOT NULL
);

CREATE TABLE execution_processes (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    workspace_id TEXT NOT NULL,
    run_reason TEXT NOT NULL,
    executor_id TEXT NOT NULL,
    executor_action JSON NOT NULL,
    status TEXT NOT NULL,
    exit_code INTEGER,
    pid INTEGER,
    started_at TIMESTAMP NOT NULL,
    finished_at TIMESTAMP,
    before_head_commit TEXT,
    after_head_commit TEXT,
    masked_by_restore BOOLEAN NOT NULL DEFAULT 0
);

CREATE TABLE execution_process_logs (
    execution_process_id TEXT NOT NULL,
    seq INTEGER NOT NULL,
    entry JSON NOT NULL,
    created_at TIMESTAMP NOT NULL,
    PRIMARY KEY (execution_process_id, seq)
);

CREATE INDEX idx_exec_proc_session ON execution_processes(session_id);
CREATE INDEX idx_workspaces_task ON workspaces(task_id);

