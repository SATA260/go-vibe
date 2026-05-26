package echo

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"syscall"

	"vibe-kanban-go/internal/executors"
)

type Executor struct{}

// New 创建内置 Echo 执行器，用于验证 harness 的进程、日志、SSE 和停止链路。
func New() *Executor {
	return &Executor{}
}

// ID 返回执行器稳定标识，落库到 sessions 和 execution_processes。
func (e *Executor) ID() string {
	return "ECHO"
}

// Name 返回执行器显示名，供前端 executor 选择器展示。
func (e *Executor) Name() string {
	return "Echo"
}

// Spawn 启动 Echo 子进程，并返回通用 Spawned 句柄。
// 业务逻辑是用 bash 跑固定 smoke 命令，把 stdout/stderr pipe 暴露给上层，同时把进程放进独立 process group，便于 Stop 一次杀掉子进程树。
func (e *Executor) Spawn(ctx context.Context, request executors.SpawnRequest) (*executors.Spawned, error) {
	cmd := exec.CommandContext(ctx, "bash", "-c", "echo hello && sleep 2 && echo done")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Dir = request.Dir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	return &executors.Spawned{
		Stdout: stdout,
		Stderr: stderr,
		Wait: func() (int, error) {
			err := cmd.Wait()
			if err == nil {
				return 0, nil
			}
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				return exitErr.ExitCode(), err
			}
			return 1, err
		},
		Kill: func() error {
			return signalProcessGroup(cmd, syscall.SIGTERM)
		},
		ForceKill: func() error {
			return signalProcessGroup(cmd, syscall.SIGKILL)
		},
		PID: cmd.Process.Pid,
	}, nil
}

// Capabilities 返回 Echo 支持的高级能力；M2a 阶段 Echo 不支持会话续写或上下文统计。
func (e *Executor) Capabilities() []executors.Capability {
	return nil
}

// Availability 返回 Echo 的可用性；它是内置 smoke executor，因此始终可用。
func (e *Executor) Availability(_ context.Context) executors.Availability {
	return executors.Availability{
		Installed: true,
		LoggedIn:  true,
		Detail:    "built-in smoke executor",
	}
}

// signalProcessGroup 向子进程所在 process group 发送信号，失败时回退到单进程 signal。
func signalProcessGroup(cmd *exec.Cmd, signal syscall.Signal) error {
	if cmd.Process == nil {
		return nil
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err == nil {
		return syscall.Kill(-pgid, signal)
	}
	if signal == syscall.SIGKILL {
		return cmd.Process.Kill()
	}
	if err := cmd.Process.Signal(signal); err != nil {
		return fmt.Errorf("signal process: %w", err)
	}
	return nil
}
