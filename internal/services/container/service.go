package container

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"

	"vibe-kanban-go/internal/msgstore"
)

type StartResponse struct {
	RepoID             string `json:"repo_id"`
	TaskID             string `json:"task_id"`
	WorkspaceID        string `json:"workspace_id"`
	SessionID          string `json:"session_id"`
	ExecutionProcessID string `json:"execution_process_id"`
}

type Service struct {
	db        *sql.DB
	stores    *msgstore.Registry
	processMu sync.Mutex
	processes map[string]*runningProcess
}

type runningProcess struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
	done   chan struct{}
}

// NewService 创建 M1a 流程服务，持有数据库、日志注册表和运行中进程表。
func NewService(db *sql.DB, stores *msgstore.Registry) *Service {
	return &Service{
		db:        db,
		stores:    stores,
		processes: make(map[string]*runningProcess),
	}
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
		workspaceID, taskID, repoID, "go-vibe/mock-"+shortID(taskID), "main", workspacePath, now,
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
	if err := s.startEcho(ctx, execID, store); err != nil {
		_ = s.updateCompletion(context.Background(), execID, "failed", nil)
		store.PushStderr(err.Error())
		store.PushFinished()
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
			store.PushFinished()
		}
	}
	return nil
}

// Stores 暴露日志注册表给路由层，用于按 execution_process_id 建立 SSE 流。
func (s *Service) Stores() *msgstore.Registry {
	return s.stores
}

// startEcho 启动 M1a 的假执行器：运行固定 bash 命令，接管 stdout/stderr，并把进程句柄登记到运行表。
// 这个阶段故意不抽象 Executor，只验证进程启动、日志捕获、DB 状态更新和 SSE 推送这条链路。
func (s *Service) startEcho(_ context.Context, execID string, store *msgstore.MsgStore) error {
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "bash", "-c", "echo hello && sleep 2 && echo done")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

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

	done := make(chan struct{})
	s.processMu.Lock()
	s.processes[execID] = &runningProcess{cmd: cmd, cancel: cancel, done: done}
	s.processMu.Unlock()

	store.PushReady()
	go streamLines(stdout, store.PushStdout)
	go streamLines(stderr, store.PushStderr)
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
		store.PushFinished()
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
		store.PushStderr(err.Error())
	}

	if err := s.updateCompletion(context.Background(), execID, status, &exitCode); err != nil {
		store.PushStderr("failed to update execution status: " + err.Error())
	}
	store.PushFinished()
}

// streamLines 按行读取 stdout/stderr，并把每一行交给 MsgStore 写入函数。
func streamLines(reader io.Reader, push func(string)) {
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		push(scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		push("stream read error: " + err.Error())
	}
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

// shortID 截取 uuid 前缀，用于生成可读的 mock 分支名。
func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

var ErrExecutionNotFound = errors.New("execution process not found")
