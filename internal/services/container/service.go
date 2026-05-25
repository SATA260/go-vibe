package container

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"

	"vibe-kanban-go/internal/msgstore"
	"vibe-kanban-go/internal/worktree"
)

type Repo struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	GitRepoPath         string `json:"git_repo_path"`
	DefaultTargetBranch string `json:"default_target_branch"`
	CreatedAt           string `json:"created_at"`
	UpdatedAt           string `json:"updated_at"`
}

type Task struct {
	ID          string `json:"id"`
	RepoID      string `json:"repo_id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Status      string `json:"status"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type BranchInfo struct {
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	Current        bool   `json:"current"`
	WorkspaceCount int    `json:"workspace_count"`
}

type RepoStructure struct {
	Repo     Repo         `json:"repo"`
	Branches []BranchInfo `json:"branches"`
}

type CreateRepoRequest struct {
	Name                string `json:"name"`
	GitRepoPath         string `json:"git_repo_path"`
	DefaultTargetBranch string `json:"default_target_branch"`
}

type CreateTaskRequest struct {
	RepoID      string `json:"repo_id"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

type StartResponse struct {
	RepoID             string `json:"repo_id"`
	TaskID             string `json:"task_id"`
	WorkspaceID        string `json:"workspace_id"`
	SessionID          string `json:"session_id"`
	ExecutionProcessID string `json:"execution_process_id"`
}

type StartWorkspaceResponse struct {
	WorkspaceID        string `json:"workspace_id"`
	SessionID          string `json:"session_id"`
	ExecutionProcessID string `json:"execution_process_id"`
	WorktreePath       string `json:"worktree_path"`
	Branch             string `json:"branch"`
}

type Session struct {
	ID              string `json:"id"`
	WorkspaceID     string `json:"workspace_id"`
	Name            string `json:"name"`
	ExecutorID      string `json:"executor_id"`
	ExecutorConfig  string `json:"executor_config"`
	AgentSessionID  string `json:"agent_session_id"`
	AgentWorkingDir string `json:"agent_working_dir"`
	CreatedAt       string `json:"created_at"`
}

type ExecutionProcess struct {
	ID               string `json:"id"`
	SessionID        string `json:"session_id"`
	WorkspaceID      string `json:"workspace_id"`
	RunReason        string `json:"run_reason"`
	ExecutorID       string `json:"executor_id"`
	ExecutorAction   string `json:"executor_action"`
	Status           string `json:"status"`
	ExitCode         *int   `json:"exit_code"`
	PID              *int   `json:"pid"`
	StartedAt        string `json:"started_at"`
	FinishedAt       string `json:"finished_at"`
	BeforeHeadCommit string `json:"before_head_commit"`
	AfterHeadCommit  string `json:"after_head_commit"`
	MaskedByRestore  bool   `json:"masked_by_restore"`
}

type WorkspaceSummary struct {
	ID              string            `json:"id"`
	TaskID          string            `json:"task_id"`
	RepoID          string            `json:"repo_id"`
	Branch          string            `json:"branch"`
	BaseBranch      string            `json:"base_branch"`
	ContainerRef    string            `json:"container_ref"`
	WorktreeDeleted bool              `json:"worktree_deleted"`
	CreatedAt       string            `json:"created_at"`
	Repo            Repo              `json:"repo"`
	Task            Task              `json:"task"`
	Session         *Session          `json:"session,omitempty"`
	LatestExecution *ExecutionProcess `json:"latest_execution,omitempty"`
}

type WorkspaceDetail struct {
	Workspace  WorkspaceSummary   `json:"workspace"`
	Sessions   []Session          `json:"sessions"`
	Executions []ExecutionProcess `json:"executions"`
}

type WorkspaceDiff struct {
	WorkspaceID string `json:"workspace_id"`
	BaseBranch  string `json:"base_branch"`
	BaseCommit  string `json:"base_commit"`
	HeadCommit  string `json:"head_commit"`
	Changed     bool   `json:"changed"`
	Stat        string `json:"stat"`
	Patch       string `json:"patch"`
}

type ExecutionLog struct {
	Seq       int              `json:"seq"`
	Kind      msgstore.LogKind `json:"kind"`
	Data      string           `json:"data"`
	CreatedAt string           `json:"created_at"`
}

type ExecutionLogsResponse struct {
	ExecutionProcessID string         `json:"execution_process_id"`
	Logs               []ExecutionLog `json:"logs"`
}

type executionLogEntry struct {
	Kind      msgstore.LogKind `json:"kind"`
	Data      string           `json:"data"`
	CreatedAt string           `json:"created_at"`
}

type Service struct {
	db        *sql.DB
	stores    *msgstore.Registry
	wt        *worktree.Manager
	processMu sync.Mutex
	logMu     sync.Mutex
	processes map[string]*runningProcess
}

type runningProcess struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
	done   chan struct{}
}

// NewService 创建执行流程服务，持有数据库、日志注册表、worktree 管理器和运行中进程表。
func NewService(db *sql.DB, stores *msgstore.Registry, wt *worktree.Manager) *Service {
	return &Service{
		db:        db,
		stores:    stores,
		wt:        wt,
		processes: make(map[string]*runningProcess),
	}
}

// CreateRepo 登记一个真实本地 git 仓库：先用 WorktreeManager 校验路径，再写入 repos 表并返回规范化后的仓库路径。
func (s *Service) CreateRepo(ctx context.Context, req CreateRepoRequest) (Repo, error) {
	name := strings.TrimSpace(req.Name)
	repoPath, err := s.wt.ResolveRepo(ctx, req.GitRepoPath)
	if err != nil {
		return Repo{}, fmt.Errorf("%w: %v", ErrBadRequest, err)
	}
	if name == "" {
		name = filepath.Base(repoPath)
	}
	defaultBranch := strings.TrimSpace(req.DefaultTargetBranch)
	if defaultBranch == "" {
		defaultBranch = "main"
	}

	now := time.Now().UTC()
	repo := Repo{
		ID:                  uuid.NewString(),
		Name:                name,
		GitRepoPath:         repoPath,
		DefaultTargetBranch: defaultBranch,
		CreatedAt:           formatTime(now),
		UpdatedAt:           formatTime(now),
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO repos (id, name, git_repo_path, default_target_branch, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		repo.ID, repo.Name, repo.GitRepoPath, repo.DefaultTargetBranch, now, now,
	)
	if err != nil {
		return Repo{}, fmt.Errorf("insert repo: %w", err)
	}
	return repo, nil
}

// ListRepos 按创建时间倒序读取已登记仓库，供前端选择 task 所属 repo。
func (s *Service) ListRepos(ctx context.Context) ([]Repo, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, git_repo_path, default_target_branch, created_at, updated_at
		FROM repos
		ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	repos := []Repo{}
	for rows.Next() {
		var repo Repo
		if err := rows.Scan(&repo.ID, &repo.Name, &repo.GitRepoPath, &repo.DefaultTargetBranch, &repo.CreatedAt, &repo.UpdatedAt); err != nil {
			return nil, err
		}
		repos = append(repos, repo)
	}
	return repos, rows.Err()
}

// ListRepoStructure 汇总所有已登记仓库的分支结构，并标注每个分支上已有多少 Go Vibe workspace。
// 业务逻辑是先读取 repos，再按 repo 调 git 获取真实分支，最后把 workspaces 表里的 branch 计数叠加进去。
func (s *Service) ListRepoStructure(ctx context.Context) ([]RepoStructure, error) {
	repos, err := s.ListRepos(ctx)
	if err != nil {
		return nil, err
	}

	structures := make([]RepoStructure, 0, len(repos))
	for _, repo := range repos {
		workspaceCounts, err := s.workspaceCountsByBranch(ctx, repo.ID)
		if err != nil {
			return nil, err
		}
		gitBranches, err := s.wt.ListBranches(ctx, repo.GitRepoPath)
		if err != nil {
			return nil, err
		}

		branchesByKey := map[string]BranchInfo{}
		for _, branch := range gitBranches {
			info := BranchInfo{
				Name:           branch.Name,
				Kind:           branch.Kind,
				Current:        branch.Current,
				WorkspaceCount: workspaceCounts[branch.Name],
			}
			branchesByKey[branch.Kind+":"+branch.Name] = info
		}
		for branchName, count := range workspaceCounts {
			key := "workspace:" + branchName
			if _, ok := branchesByKey["local:"+branchName]; ok {
				continue
			}
			branchesByKey[key] = BranchInfo{Name: branchName, Kind: "workspace", WorkspaceCount: count}
		}

		branches := make([]BranchInfo, 0, len(branchesByKey))
		for _, branch := range branchesByKey {
			branches = append(branches, branch)
		}
		sortBranches(branches)
		structures = append(structures, RepoStructure{Repo: repo, Branches: branches})
	}
	return structures, nil
}

// CreateTask 在指定 repo 下创建待执行任务；M1 只保存标题、描述和 todo 状态，不做看板流转。
func (s *Service) CreateTask(ctx context.Context, req CreateTaskRequest) (Task, error) {
	repoID := strings.TrimSpace(req.RepoID)
	title := strings.TrimSpace(req.Title)
	if repoID == "" || title == "" {
		return Task{}, fmt.Errorf("%w: repo_id and title are required", ErrBadRequest)
	}
	if _, err := s.getRepo(ctx, repoID); err != nil {
		return Task{}, err
	}

	now := time.Now().UTC()
	task := Task{
		ID:          uuid.NewString(),
		RepoID:      repoID,
		Title:       title,
		Description: strings.TrimSpace(req.Description),
		Status:      "todo",
		CreatedAt:   formatTime(now),
		UpdatedAt:   formatTime(now),
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO tasks (id, repo_id, title, description, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		task.ID, task.RepoID, task.Title, task.Description, task.Status, now, now,
	)
	if err != nil {
		return Task{}, fmt.Errorf("insert task: %w", err)
	}
	return task, nil
}

// ListTasks 按 repo_id 读取任务列表；如果 repo_id 为空，返回所有任务用于本地调试。
func (s *Service) ListTasks(ctx context.Context, repoID string) ([]Task, error) {
	var rows *sql.Rows
	var err error
	if strings.TrimSpace(repoID) == "" {
		rows, err = s.db.QueryContext(ctx, `
			SELECT id, repo_id, title, COALESCE(description, ''), status, created_at, updated_at
			FROM tasks
			ORDER BY created_at DESC`)
	} else {
		rows, err = s.db.QueryContext(ctx, `
			SELECT id, repo_id, title, COALESCE(description, ''), status, created_at, updated_at
			FROM tasks
			WHERE repo_id = ?
			ORDER BY created_at DESC`, repoID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tasks := []Task{}
	for rows.Next() {
		var task Task
		if err := rows.Scan(&task.ID, &task.RepoID, &task.Title, &task.Description, &task.Status, &task.CreatedAt, &task.UpdatedAt); err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

// ListWorkspaces 读取 workspace 审查列表，附带 repo、task、最新 session 和最新 execution 摘要。
// 业务上这个列表是前端 Review Console 的入口，让用户能从任务维度快速定位 worktree、分支和执行状态。
func (s *Service) ListWorkspaces(ctx context.Context) ([]WorkspaceSummary, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			w.id, w.task_id, w.repo_id, w.branch, w.base_branch, w.container_ref, w.worktree_deleted, w.created_at,
			r.id, r.name, r.git_repo_path, r.default_target_branch, r.created_at, r.updated_at,
			t.id, t.repo_id, t.title, COALESCE(t.description, ''), t.status, t.created_at, t.updated_at,
			COALESCE(s.id, ''), COALESCE(s.workspace_id, ''), COALESCE(s.name, ''), COALESCE(s.executor_id, ''),
			COALESCE(s.executor_config, ''), COALESCE(s.agent_session_id, ''), COALESCE(s.agent_working_dir, ''), COALESCE(s.created_at, ''),
			COALESCE(ep.id, ''), COALESCE(ep.session_id, ''), COALESCE(ep.workspace_id, ''), COALESCE(ep.run_reason, ''),
			COALESCE(ep.executor_id, ''), COALESCE(ep.executor_action, ''), COALESCE(ep.status, ''), ep.exit_code, ep.pid,
			COALESCE(ep.started_at, ''), COALESCE(ep.finished_at, ''), COALESCE(ep.before_head_commit, ''),
			COALESCE(ep.after_head_commit, ''), COALESCE(ep.masked_by_restore, 0)
		FROM workspaces w
		JOIN repos r ON r.id = w.repo_id
		JOIN tasks t ON t.id = w.task_id
		LEFT JOIN sessions s ON s.id = (
			SELECT id FROM sessions WHERE workspace_id = w.id ORDER BY created_at DESC LIMIT 1
		)
		LEFT JOIN execution_processes ep ON ep.id = (
			SELECT id FROM execution_processes WHERE workspace_id = w.id ORDER BY started_at DESC LIMIT 1
		)
		ORDER BY w.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	workspaces := []WorkspaceSummary{}
	for rows.Next() {
		workspace, err := scanWorkspaceSummary(rows)
		if err != nil {
			return nil, err
		}
		workspaces = append(workspaces, workspace)
	}
	return workspaces, rows.Err()
}

// GetWorkspace 读取单个 workspace 详情，并补齐它下面的 sessions 和 execution_processes。
// 业务逻辑分三步：先复用列表查询拿基础摘要，再查会话列表，最后查执行列表，供审查页一次性渲染上下文。
func (s *Service) GetWorkspace(ctx context.Context, workspaceID string) (WorkspaceDetail, error) {
	summary, err := s.getWorkspaceSummary(ctx, workspaceID)
	if err != nil {
		return WorkspaceDetail{}, err
	}
	sessions, err := s.listSessions(ctx, workspaceID)
	if err != nil {
		return WorkspaceDetail{}, err
	}
	executions, err := s.listExecutionsByWorkspace(ctx, workspaceID)
	if err != nil {
		return WorkspaceDetail{}, err
	}
	return WorkspaceDetail{Workspace: summary, Sessions: sessions, Executions: executions}, nil
}

// GetWorkspaceDiff 读取 workspace 记录并调用 git 计算当前 worktree 相对 base branch 的 raw diff。
// 这个接口只做审查视图，不会修改 worktree；如果没有任何文件变化，会返回 changed=false 和空 stat/patch。
func (s *Service) GetWorkspaceDiff(ctx context.Context, workspaceID string) (WorkspaceDiff, error) {
	summary, err := s.getWorkspaceSummary(ctx, workspaceID)
	if err != nil {
		return WorkspaceDiff{}, err
	}
	diff, err := s.wt.Diff(ctx, summary.ContainerRef, summary.BaseBranch)
	if err != nil {
		return WorkspaceDiff{}, err
	}
	return WorkspaceDiff{
		WorkspaceID: workspaceID,
		BaseBranch:  summary.BaseBranch,
		BaseCommit:  diff.BaseCommit,
		HeadCommit:  diff.HeadCommit,
		Changed:     diff.Changed,
		Stat:        diff.Stat,
		Patch:       diff.Patch,
	}, nil
}

// GetExecutionProcess 读取单个 execution_process 详情；不存在时返回 ErrExecutionNotFound。
func (s *Service) GetExecutionProcess(ctx context.Context, execID string) (ExecutionProcess, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, session_id, workspace_id, run_reason, executor_id, executor_action, status,
			exit_code, pid, started_at, finished_at, before_head_commit, after_head_commit, masked_by_restore
		FROM execution_processes
		WHERE id = ?`, execID)
	process, err := scanExecutionProcess(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ExecutionProcess{}, ErrExecutionNotFound
	}
	return process, err
}

// GetExecutionLogs 从 SQLite 读取指定 execution_process 的历史日志，供刷新后回放和调试接口使用。
func (s *Service) GetExecutionLogs(ctx context.Context, execID string) (ExecutionLogsResponse, error) {
	if _, err := s.GetExecutionProcess(ctx, execID); err != nil {
		return ExecutionLogsResponse{}, err
	}
	logs, err := s.listExecutionLogs(ctx, execID)
	if err != nil {
		return ExecutionLogsResponse{}, err
	}
	return ExecutionLogsResponse{ExecutionProcessID: execID, Logs: logs}, nil
}

// StartWorkspace 启动真实 M1 执行链路：读取 task/repo，创建 git worktree，落库 workspace/session/process，并在 worktree 中启动 Echo。
// 这个函数是 mock flow 到真实 harness 的第一条主线；任何一步失败都会阻止进程启动，避免出现没有 worktree 的脏 execution_process。
func (s *Service) StartWorkspace(ctx context.Context, taskID string) (StartWorkspaceResponse, error) {
	task, err := s.getTask(ctx, taskID)
	if err != nil {
		return StartWorkspaceResponse{}, err
	}
	repo, err := s.getRepo(ctx, task.RepoID)
	if err != nil {
		return StartWorkspaceResponse{}, err
	}

	now := time.Now().UTC()
	workspaceID := uuid.NewString()
	sessionID := uuid.NewString()
	execID := uuid.NewString()
	branch := generateWorkspaceBranch(task.ID)
	worktreePath := filepath.Join(s.wt.RootDir(), workspaceID)

	if err := s.wt.Create(ctx, repo.GitRepoPath, branch, worktreePath, repo.DefaultTargetBranch); err != nil {
		return StartWorkspaceResponse{}, fmt.Errorf("create worktree: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return StartWorkspaceResponse{}, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO workspaces (id, task_id, repo_id, branch, base_branch, container_ref, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		workspaceID, task.ID, repo.ID, branch, repo.DefaultTargetBranch, worktreePath, now,
	); err != nil {
		return StartWorkspaceResponse{}, fmt.Errorf("insert workspace: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO sessions (id, workspace_id, name, executor_id, executor_config, agent_working_dir, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		sessionID, workspaceID, "Echo", "ECHO", `{}`, worktreePath, now,
	); err != nil {
		return StartWorkspaceResponse{}, fmt.Errorf("insert session: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO execution_processes (
			id, session_id, workspace_id, run_reason, executor_id, executor_action,
			status, started_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		execID, sessionID, workspaceID, "coding_agent", "ECHO", `{"type":"echo"}`, "running", now,
	); err != nil {
		return StartWorkspaceResponse{}, fmt.Errorf("insert execution process: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return StartWorkspaceResponse{}, err
	}

	store := s.stores.Create(execID)
	if err := s.startEcho(ctx, execID, store, worktreePath); err != nil {
		_ = s.updateCompletion(context.Background(), execID, "failed", nil)
		s.pushExecutionLog(context.Background(), execID, store, msgstore.KindStderr, err.Error())
		s.pushExecutionLog(context.Background(), execID, store, msgstore.KindFinished, "")
		return StartWorkspaceResponse{}, err
	}

	return StartWorkspaceResponse{
		WorkspaceID:        workspaceID,
		SessionID:          sessionID,
		ExecutionProcessID: execID,
		WorktreePath:       worktreePath,
		Branch:             branch,
	}, nil
}

// StartMock 跑通一次 mock 执行业务流：先插入 repo/task/workspace/session/execution_process，再创建 MsgStore，最后启动 echo 子进程。
// 这里不做真实 git worktree 和真实 agent，只用默认字段填充现有表，让前端到后端到 SSE 的流程先闭环。
func (s *Service) StartMock(ctx context.Context) (StartResponse, error) {
	now := time.Now().UTC()
	repoID := uuid.NewString()
	taskID := uuid.NewString()
	workspaceID := uuid.NewString()
	sessionID := uuid.NewString()
	execID := uuid.NewString()

	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	workspacePath := filepath.Join("worktrees", workspaceID)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return StartResponse{}, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO repos (id, name, git_repo_path, default_target_branch, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		repoID, "mock-repo", cwd, "main", now, now,
	); err != nil {
		return StartResponse{}, fmt.Errorf("insert repo: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO tasks (id, repo_id, title, description, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		taskID, repoID, "Mock echo task", "M1a flow smoke test", "todo", now, now,
	); err != nil {
		return StartResponse{}, fmt.Errorf("insert task: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO workspaces (id, task_id, repo_id, branch, base_branch, container_ref, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		workspaceID, taskID, repoID, generateMockBranch(taskID), "main", workspacePath, now,
	); err != nil {
		return StartResponse{}, fmt.Errorf("insert workspace: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO sessions (id, workspace_id, name, executor_id, executor_config, agent_working_dir, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		sessionID, workspaceID, "Mock Echo", "ECHO", `{}`, workspacePath, now,
	); err != nil {
		return StartResponse{}, fmt.Errorf("insert session: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO execution_processes (
			id, session_id, workspace_id, run_reason, executor_id, executor_action,
			status, started_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		execID, sessionID, workspaceID, "coding_agent", "ECHO", `{"type":"echo"}`, "running", now,
	); err != nil {
		return StartResponse{}, fmt.Errorf("insert execution process: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return StartResponse{}, err
	}

	store := s.stores.Create(execID)
	if err := s.startEcho(ctx, execID, store, "."); err != nil {
		_ = s.updateCompletion(context.Background(), execID, "failed", nil)
		s.pushExecutionLog(context.Background(), execID, store, msgstore.KindStderr, err.Error())
		s.pushExecutionLog(context.Background(), execID, store, msgstore.KindFinished, "")
		return StartResponse{}, err
	}

	return StartResponse{
		RepoID:             repoID,
		TaskID:             taskID,
		WorkspaceID:        workspaceID,
		SessionID:          sessionID,
		ExecutionProcessID: execID,
	}, nil
}

// Stop 停止指定执行进程：先把 DB 状态更新为 killed，再向进程组发终止信号，最后等待 wait goroutine 推送 finished。
// 如果优雅停止超时，会补发 SIGKILL 并兜底推送 finished，避免前端 SSE 一直挂起。
func (s *Service) Stop(ctx context.Context, execID string) error {
	s.processMu.Lock()
	process, ok := s.processes[execID]
	s.processMu.Unlock()
	if !ok {
		var status string
		err := s.db.QueryRowContext(ctx, `SELECT status FROM execution_processes WHERE id = ?`, execID).Scan(&status)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrExecutionNotFound
		}
		return err
	}

	if err := s.updateCompletion(ctx, execID, "killed", nil); err != nil {
		return err
	}

	timedOut := false
	if process.cmd.Process != nil {
		pgid, err := syscall.Getpgid(process.cmd.Process.Pid)
		if err == nil {
			_ = syscall.Kill(-pgid, syscall.SIGTERM)
			select {
			case <-process.done:
			case <-time.After(1500 * time.Millisecond):
				_ = syscall.Kill(-pgid, syscall.SIGKILL)
				timedOut = true
			}
		} else {
			process.cancel()
			_ = process.cmd.Process.Kill()
		}
	} else {
		process.cancel()
	}

	select {
	case <-process.done:
	case <-time.After(2 * time.Second):
		timedOut = true
	}

	if timedOut {
		process.cancel()
		if store, ok := s.stores.Get(execID); ok {
			s.pushExecutionLog(context.Background(), execID, store, msgstore.KindFinished, "")
		}
	}
	return nil
}

// Stores 暴露日志注册表给路由层，用于按 execution_process_id 建立 SSE 流。
func (s *Service) Stores() *msgstore.Registry {
	return s.stores
}

// startEcho 启动 M1 的 Echo 执行器：运行固定 bash 命令，接管 stdout/stderr，并把进程句柄登记到运行表。
// 这个阶段故意不抽象 Executor，只验证进程启动、日志捕获、DB 状态更新和 SSE 推送这条链路。
func (s *Service) startEcho(_ context.Context, execID string, store *msgstore.MsgStore, workingDir string) error {
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "bash", "-c", "echo hello && sleep 2 && echo done")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Dir = workingDir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return err
	}
	if cmd.Process != nil {
		_, _ = s.db.ExecContext(context.Background(), `UPDATE execution_processes SET pid = ? WHERE id = ?`, cmd.Process.Pid, execID)
	}

	done := make(chan struct{})
	s.processMu.Lock()
	s.processes[execID] = &runningProcess{cmd: cmd, cancel: cancel, done: done}
	s.processMu.Unlock()

	s.pushExecutionLog(context.Background(), execID, store, msgstore.KindReady, "")
	go streamLines(stdout, func(line string) {
		s.pushExecutionLog(context.Background(), execID, store, msgstore.KindStdout, line)
	})
	go streamLines(stderr, func(line string) {
		s.pushExecutionLog(context.Background(), execID, store, msgstore.KindStderr, line)
	})
	go s.waitForEcho(execID, cmd, cancel, done, store)

	return nil
}

// waitForEcho 等待 echo 进程结束并收敛状态：被 Stop 标记为 killed 时只推 finished；自然退出时按退出码更新 completed/failed。
// 无论哪种结束方式，它都会清理运行中进程表并给 SSE 客户端发送 finished。
func (s *Service) waitForEcho(execID string, cmd *exec.Cmd, cancel context.CancelFunc, done chan struct{}, store *msgstore.MsgStore) {
	defer close(done)
	defer cancel()
	defer func() {
		s.processMu.Lock()
		delete(s.processes, execID)
		s.processMu.Unlock()
	}()

	err := cmd.Wait()

	var currentStatus string
	statusErr := s.db.QueryRowContext(context.Background(), `SELECT status FROM execution_processes WHERE id = ?`, execID).Scan(&currentStatus)
	if statusErr == nil && currentStatus == "killed" {
		s.pushExecutionLog(context.Background(), execID, store, msgstore.KindFinished, "")
		return
	}

	exitCode := 0
	status := "completed"
	if err != nil {
		status = "failed"
		exitCode = 1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
		s.pushExecutionLog(context.Background(), execID, store, msgstore.KindStderr, err.Error())
	}

	if err := s.updateCompletion(context.Background(), execID, status, &exitCode); err != nil {
		s.pushExecutionLog(context.Background(), execID, store, msgstore.KindStderr, "failed to update execution status: "+err.Error())
	}
	s.pushExecutionLog(context.Background(), execID, store, msgstore.KindFinished, "")
}

// streamLines 按行读取 stdout/stderr，并把每一行交给 MsgStore 写入函数。
func streamLines(reader io.Reader, push func(string)) {
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		push(scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, os.ErrClosed) || strings.Contains(err.Error(), "file already closed") {
			return
		}
		push("stream read error: " + err.Error())
	}
}

// pushExecutionLog 把执行日志同时写入 SQLite 和内存 MsgStore。
// 业务步骤是先在互斥锁内计算下一条 seq 并落库，再带着 seq 广播给 SSE；即使落库失败，也会推送实时 stderr 方便前端看到问题。
func (s *Service) pushExecutionLog(ctx context.Context, execID string, store *msgstore.MsgStore, kind msgstore.LogKind, data string) {
	log, err := s.insertExecutionLog(ctx, execID, kind, data)
	if err != nil {
		store.Push(msgstore.LogMsg{Kind: kind, Data: data})
		if kind != msgstore.KindStderr {
			store.Push(msgstore.LogMsg{Kind: msgstore.KindStderr, Data: "failed to persist execution log: " + err.Error()})
		}
		return
	}
	store.Push(msgstore.LogMsg{Kind: log.Kind, Data: log.Data, Seq: log.Seq})
}

// insertExecutionLog 在事务中追加一条 execution_process_logs 记录，并返回写入后的 seq。
// 这里用服务层互斥锁串行化同一进程的 max(seq)+1 计算，避免 stdout/stderr 两个 goroutine 并发写出重复序号。
func (s *Service) insertExecutionLog(ctx context.Context, execID string, kind msgstore.LogKind, data string) (ExecutionLog, error) {
	now := time.Now().UTC()
	entry := executionLogEntry{Kind: kind, Data: data, CreatedAt: formatTime(now)}
	rawEntry, err := json.Marshal(entry)
	if err != nil {
		return ExecutionLog{}, err
	}

	s.logMu.Lock()
	defer s.logMu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ExecutionLog{}, err
	}
	defer tx.Rollback()

	var seq int
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(seq), 0) + 1
		FROM execution_process_logs
		WHERE execution_process_id = ?`, execID).Scan(&seq); err != nil {
		return ExecutionLog{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO execution_process_logs (execution_process_id, seq, entry, created_at)
		VALUES (?, ?, ?, ?)`, execID, seq, string(rawEntry), now); err != nil {
		return ExecutionLog{}, err
	}
	if err := tx.Commit(); err != nil {
		return ExecutionLog{}, err
	}
	return ExecutionLog{Seq: seq, Kind: kind, Data: data, CreatedAt: entry.CreatedAt}, nil
}

// updateCompletion 更新 execution_processes 的最终状态、退出码和 finished_at。
func (s *Service) updateCompletion(ctx context.Context, execID, status string, exitCode *int) error {
	var value any
	if exitCode != nil {
		value = *exitCode
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE execution_processes
		SET status = ?, exit_code = ?, finished_at = ?
		WHERE id = ?`,
		status, value, time.Now().UTC(), execID,
	)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrExecutionNotFound
	}
	return nil
}

// generateWorkspaceBranch 生成真实 worktree 分支名，既保留 task 前缀方便追踪，又加入随机段避免重复启动同一 task 时撞分支。
func generateWorkspaceBranch(taskID string) string {
	return "go-vibe/" + shortID(taskID) + "-" + shortID(uuid.NewString())
}

// generateMockBranch 生成 mock workspace 分支名；mock 不创建真实 git 分支，但仍保持和真实链路一致的唯一命名。
func generateMockBranch(taskID string) string {
	return "go-vibe/mock-" + shortID(taskID) + "-" + shortID(uuid.NewString())
}

// shortID 截取 uuid 前缀，用于生成可读的分支名和调试标识。
func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

type rowScanner interface {
	Scan(dest ...any) error
}

// getWorkspaceSummary 读取单个 workspace 审查摘要；不存在时返回 ErrWorkspaceNotFound。
func (s *Service) getWorkspaceSummary(ctx context.Context, workspaceID string) (WorkspaceSummary, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT
			w.id, w.task_id, w.repo_id, w.branch, w.base_branch, w.container_ref, w.worktree_deleted, w.created_at,
			r.id, r.name, r.git_repo_path, r.default_target_branch, r.created_at, r.updated_at,
			t.id, t.repo_id, t.title, COALESCE(t.description, ''), t.status, t.created_at, t.updated_at,
			COALESCE(s.id, ''), COALESCE(s.workspace_id, ''), COALESCE(s.name, ''), COALESCE(s.executor_id, ''),
			COALESCE(s.executor_config, ''), COALESCE(s.agent_session_id, ''), COALESCE(s.agent_working_dir, ''), COALESCE(s.created_at, ''),
			COALESCE(ep.id, ''), COALESCE(ep.session_id, ''), COALESCE(ep.workspace_id, ''), COALESCE(ep.run_reason, ''),
			COALESCE(ep.executor_id, ''), COALESCE(ep.executor_action, ''), COALESCE(ep.status, ''), ep.exit_code, ep.pid,
			COALESCE(ep.started_at, ''), COALESCE(ep.finished_at, ''), COALESCE(ep.before_head_commit, ''),
			COALESCE(ep.after_head_commit, ''), COALESCE(ep.masked_by_restore, 0)
		FROM workspaces w
		JOIN repos r ON r.id = w.repo_id
		JOIN tasks t ON t.id = w.task_id
		LEFT JOIN sessions s ON s.id = (
			SELECT id FROM sessions WHERE workspace_id = w.id ORDER BY created_at DESC LIMIT 1
		)
		LEFT JOIN execution_processes ep ON ep.id = (
			SELECT id FROM execution_processes WHERE workspace_id = w.id ORDER BY started_at DESC LIMIT 1
		)
		WHERE w.id = ?`, workspaceID)
	workspace, err := scanWorkspaceSummary(row)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkspaceSummary{}, ErrWorkspaceNotFound
	}
	return workspace, err
}

// scanWorkspaceSummary 把 workspace/repo/task/session/execution 的 JOIN 结果组装成前端审查摘要。
func scanWorkspaceSummary(scanner rowScanner) (WorkspaceSummary, error) {
	var workspace WorkspaceSummary
	var session Session
	var execution ExecutionProcess
	var sessionID string
	var executionID string
	var exitCode sql.NullInt64
	var pid sql.NullInt64

	err := scanner.Scan(
		&workspace.ID, &workspace.TaskID, &workspace.RepoID, &workspace.Branch, &workspace.BaseBranch,
		&workspace.ContainerRef, &workspace.WorktreeDeleted, &workspace.CreatedAt,
		&workspace.Repo.ID, &workspace.Repo.Name, &workspace.Repo.GitRepoPath, &workspace.Repo.DefaultTargetBranch,
		&workspace.Repo.CreatedAt, &workspace.Repo.UpdatedAt,
		&workspace.Task.ID, &workspace.Task.RepoID, &workspace.Task.Title, &workspace.Task.Description,
		&workspace.Task.Status, &workspace.Task.CreatedAt, &workspace.Task.UpdatedAt,
		&sessionID, &session.WorkspaceID, &session.Name, &session.ExecutorID, &session.ExecutorConfig,
		&session.AgentSessionID, &session.AgentWorkingDir, &session.CreatedAt,
		&executionID, &execution.SessionID, &execution.WorkspaceID, &execution.RunReason, &execution.ExecutorID,
		&execution.ExecutorAction, &execution.Status, &exitCode, &pid, &execution.StartedAt, &execution.FinishedAt,
		&execution.BeforeHeadCommit, &execution.AfterHeadCommit, &execution.MaskedByRestore,
	)
	if err != nil {
		return WorkspaceSummary{}, err
	}
	workspace.TaskID = workspace.Task.ID
	workspace.RepoID = workspace.Repo.ID
	if sessionID != "" {
		session.ID = sessionID
		workspace.Session = &session
	}
	if executionID != "" {
		execution.ID = executionID
		if exitCode.Valid {
			value := int(exitCode.Int64)
			execution.ExitCode = &value
		}
		if pid.Valid {
			value := int(pid.Int64)
			execution.PID = &value
		}
		workspace.LatestExecution = &execution
	}
	return workspace, nil
}

// listSessions 读取 workspace 下的所有 session，按创建时间倒序返回。
func (s *Service) listSessions(ctx context.Context, workspaceID string) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, workspace_id, COALESCE(name, ''), executor_id, executor_config,
			COALESCE(agent_session_id, ''), COALESCE(agent_working_dir, ''), created_at
		FROM sessions
		WHERE workspace_id = ?
		ORDER BY created_at DESC`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	sessions := []Session{}
	for rows.Next() {
		var session Session
		if err := rows.Scan(&session.ID, &session.WorkspaceID, &session.Name, &session.ExecutorID, &session.ExecutorConfig, &session.AgentSessionID, &session.AgentWorkingDir, &session.CreatedAt); err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

// listExecutionsByWorkspace 读取 workspace 下的执行记录，按开始时间倒序返回。
func (s *Service) listExecutionsByWorkspace(ctx context.Context, workspaceID string) ([]ExecutionProcess, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, session_id, workspace_id, run_reason, executor_id, executor_action, status,
			exit_code, pid, started_at, finished_at, before_head_commit, after_head_commit, masked_by_restore
		FROM execution_processes
		WHERE workspace_id = ?
		ORDER BY started_at DESC`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	processes := []ExecutionProcess{}
	for rows.Next() {
		process, err := scanExecutionProcess(rows)
		if err != nil {
			return nil, err
		}
		processes = append(processes, process)
	}
	return processes, rows.Err()
}

// scanExecutionProcess 把 execution_processes 的单行数据转换为 API 对象，并保留 nullable 的 pid/exit_code。
func scanExecutionProcess(scanner rowScanner) (ExecutionProcess, error) {
	var process ExecutionProcess
	var exitCode sql.NullInt64
	var pid sql.NullInt64
	var finishedAt sql.NullString
	var beforeHead sql.NullString
	var afterHead sql.NullString
	err := scanner.Scan(
		&process.ID, &process.SessionID, &process.WorkspaceID, &process.RunReason, &process.ExecutorID,
		&process.ExecutorAction, &process.Status, &exitCode, &pid, &process.StartedAt, &finishedAt,
		&beforeHead, &afterHead, &process.MaskedByRestore,
	)
	if err != nil {
		return ExecutionProcess{}, err
	}
	if exitCode.Valid {
		value := int(exitCode.Int64)
		process.ExitCode = &value
	}
	if pid.Valid {
		value := int(pid.Int64)
		process.PID = &value
	}
	if finishedAt.Valid {
		process.FinishedAt = finishedAt.String
	}
	if beforeHead.Valid {
		process.BeforeHeadCommit = beforeHead.String
	}
	if afterHead.Valid {
		process.AfterHeadCommit = afterHead.String
	}
	return process, nil
}

// listExecutionLogs 读取并解析 execution_process_logs.entry，兼容后续日志回放和独立 logs API。
func (s *Service) listExecutionLogs(ctx context.Context, execID string) ([]ExecutionLog, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT seq, entry, created_at
		FROM execution_process_logs
		WHERE execution_process_id = ?
		ORDER BY seq ASC`, execID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	logs := []ExecutionLog{}
	for rows.Next() {
		var seq int
		var rawEntry string
		var createdAt string
		if err := rows.Scan(&seq, &rawEntry, &createdAt); err != nil {
			return nil, err
		}
		var entry executionLogEntry
		if err := json.Unmarshal([]byte(rawEntry), &entry); err != nil {
			return nil, fmt.Errorf("parse execution log %d: %w", seq, err)
		}
		if entry.CreatedAt == "" {
			entry.CreatedAt = createdAt
		}
		logs = append(logs, ExecutionLog{Seq: seq, Kind: entry.Kind, Data: entry.Data, CreatedAt: entry.CreatedAt})
	}
	return logs, rows.Err()
}

// getRepo 读取指定 repo 记录；不存在时返回 ErrRepoNotFound。
func (s *Service) getRepo(ctx context.Context, repoID string) (Repo, error) {
	var repo Repo
	err := s.db.QueryRowContext(ctx, `
		SELECT id, name, git_repo_path, default_target_branch, created_at, updated_at
		FROM repos
		WHERE id = ?`, repoID,
	).Scan(&repo.ID, &repo.Name, &repo.GitRepoPath, &repo.DefaultTargetBranch, &repo.CreatedAt, &repo.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Repo{}, ErrRepoNotFound
	}
	if err != nil {
		return Repo{}, err
	}
	return repo, nil
}

// getTask 读取指定 task 记录；不存在时返回 ErrTaskNotFound。
func (s *Service) getTask(ctx context.Context, taskID string) (Task, error) {
	var task Task
	err := s.db.QueryRowContext(ctx, `
		SELECT id, repo_id, title, COALESCE(description, ''), status, created_at, updated_at
		FROM tasks
		WHERE id = ?`, taskID,
	).Scan(&task.ID, &task.RepoID, &task.Title, &task.Description, &task.Status, &task.CreatedAt, &task.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrTaskNotFound
	}
	if err != nil {
		return Task{}, err
	}
	return task, nil
}

// workspaceCountsByBranch 统计某个 repo 下每个分支关联的 workspace 数量，用于结构图标注 Go Vibe 创建过的分支。
func (s *Service) workspaceCountsByBranch(ctx context.Context, repoID string) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT branch, COUNT(*)
		FROM workspaces
		WHERE repo_id = ?
		GROUP BY branch`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	counts := map[string]int{}
	for rows.Next() {
		var branch string
		var count int
		if err := rows.Scan(&branch, &count); err != nil {
			return nil, err
		}
		counts[branch] = count
	}
	return counts, rows.Err()
}

// sortBranches 让分支结构展示稳定：当前分支优先，其次本地、workspace、远端，最后按名称排序。
func sortBranches(branches []BranchInfo) {
	kindRank := map[string]int{"local": 0, "workspace": 1, "remote": 2}
	sort.Slice(branches, func(i, j int) bool {
		if branches[i].Current != branches[j].Current {
			return branches[i].Current
		}
		leftRank := kindRank[branches[i].Kind]
		rightRank := kindRank[branches[j].Kind]
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		return branches[i].Name < branches[j].Name
	})
}

// formatTime 把服务层时间统一序列化为 RFC3339，便于前端直接展示和调试。
func formatTime(t time.Time) string {
	return t.Format(time.RFC3339Nano)
}

var ErrExecutionNotFound = errors.New("execution process not found")
var ErrRepoNotFound = errors.New("repo not found")
var ErrTaskNotFound = errors.New("task not found")
var ErrWorkspaceNotFound = errors.New("workspace not found")
var ErrBadRequest = errors.New("bad request")
