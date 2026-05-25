package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

var ErrWorktreePathExists = errors.New("worktree path already exists")

type Manager struct {
	rootDir string
	locks   sync.Map
}

type Branch struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Current bool   `json:"current"`
}

type Diff struct {
	BaseCommit string `json:"base_commit"`
	HeadCommit string `json:"head_commit"`
	Changed    bool   `json:"changed"`
	Stat       string `json:"stat"`
	Patch      string `json:"patch"`
}

// NewManager 创建 worktree 管理器，rootDir 是所有任务隔离目录的根路径。
func NewManager(rootDir string) *Manager {
	return &Manager{rootDir: rootDir}
}

// RootDir 返回 worktree 根目录，主要给服务层生成 workspace 路径使用。
func (m *Manager) RootDir() string {
	return m.rootDir
}

// ResolveRepo 校验传入路径是否为 git 仓库，并返回 git 识别到的仓库顶层目录。
func (m *Manager) ResolveRepo(ctx context.Context, repoPath string) (string, error) {
	trimmed := strings.TrimSpace(repoPath)
	if trimmed == "" {
		return "", errors.New("git_repo_path is required")
	}

	absPath, err := filepath.Abs(trimmed)
	if err != nil {
		return "", fmt.Errorf("resolve repo path: %w", err)
	}

	cmd := exec.CommandContext(ctx, "git", "-C", absPath, "rev-parse", "--show-toplevel")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("path is not a git repository: %s", strings.TrimSpace(string(output)))
	}

	root := strings.TrimSpace(string(output))
	if root == "" {
		return "", errors.New("git returned an empty repository root")
	}
	return root, nil
}

// Create 在 rootDir 下创建真实 git worktree。
// 业务流程：按 repo path 加锁，拒绝复用已存在的目标目录，确保父目录存在，然后执行 git worktree add。
// 这里不自动删除已有目录，避免误删用户文件；如果 git 创建失败，会把 stderr 返回给 API 调用方。
func (m *Manager) Create(ctx context.Context, repoPath, branchName, worktreePath, baseBranch string) error {
	repoRoot, err := m.ResolveRepo(ctx, repoPath)
	if err != nil {
		return err
	}

	lock := m.lockFor(repoRoot)
	lock.Lock()
	defer lock.Unlock()

	absWorktree, err := filepath.Abs(worktreePath)
	if err != nil {
		return fmt.Errorf("resolve worktree path: %w", err)
	}
	if _, err := os.Stat(absWorktree); err == nil {
		return fmt.Errorf("%w: %s", ErrWorktreePathExists, absWorktree)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("check worktree path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(absWorktree), 0o755); err != nil {
		return fmt.Errorf("create worktree parent: %w", err)
	}

	target := strings.TrimSpace(baseBranch)
	if target == "" {
		target = "main"
	}
	cmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "worktree", "add", "-b", branchName, absWorktree, target)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git worktree add failed: %s", strings.TrimSpace(string(output)))
	}
	return nil
}

// ListBranches 读取仓库的本地和远端分支，用于前端展示仓库结构图。
// 业务逻辑是先解析仓库根目录，再分别调用 git branch 本地/远端命令，并过滤 origin/HEAD 这类符号引用。
func (m *Manager) ListBranches(ctx context.Context, repoPath string) ([]Branch, error) {
	repoRoot, err := m.ResolveRepo(ctx, repoPath)
	if err != nil {
		return nil, err
	}

	branchesByKey := map[string]Branch{}
	localOutput, err := exec.CommandContext(ctx, "git", "-C", repoRoot, "branch", "--format=%(refname:short)|%(HEAD)").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("list local branches failed: %s", strings.TrimSpace(string(localOutput)))
	}
	for _, line := range strings.Split(strings.TrimSpace(string(localOutput)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 2)
		name := strings.TrimSpace(parts[0])
		current := len(parts) > 1 && strings.TrimSpace(parts[1]) == "*"
		branchesByKey["local:"+name] = Branch{Name: name, Kind: "local", Current: current}
	}

	remoteOutput, err := exec.CommandContext(ctx, "git", "-C", repoRoot, "branch", "-r", "--format=%(refname:short)").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("list remote branches failed: %s", strings.TrimSpace(string(remoteOutput)))
	}
	for _, line := range strings.Split(strings.TrimSpace(string(remoteOutput)), "\n") {
		name := strings.TrimSpace(line)
		if name == "" || strings.Contains(name, "->") {
			continue
		}
		branchesByKey["remote:"+name] = Branch{Name: name, Kind: "remote"}
	}

	branches := make([]Branch, 0, len(branchesByKey))
	for _, branch := range branchesByKey {
		branches = append(branches, branch)
	}
	return branches, nil
}

// Diff 计算 worktree 相对 baseBranch 的文本差异，用于 M1.5 的审查面板。
// 业务流程是先确认目录是 git worktree，再用 merge-base 找共同祖先，最后分别读取 diff stat 和 raw patch。
func (m *Manager) Diff(ctx context.Context, worktreePath, baseBranch string) (Diff, error) {
	trimmed := strings.TrimSpace(worktreePath)
	if trimmed == "" {
		return Diff{}, errors.New("worktree path is required")
	}
	absWorktree, err := filepath.Abs(trimmed)
	if err != nil {
		return Diff{}, fmt.Errorf("resolve worktree path: %w", err)
	}
	if output, err := exec.CommandContext(ctx, "git", "-C", absWorktree, "rev-parse", "--show-toplevel").CombinedOutput(); err != nil {
		return Diff{}, fmt.Errorf("path is not a git worktree: %s", strings.TrimSpace(string(output)))
	}

	target := strings.TrimSpace(baseBranch)
	if target == "" {
		target = "main"
	}
	baseOutput, err := exec.CommandContext(ctx, "git", "-C", absWorktree, "merge-base", "HEAD", target).CombinedOutput()
	if err != nil {
		return Diff{}, fmt.Errorf("git merge-base failed: %s", strings.TrimSpace(string(baseOutput)))
	}
	baseCommit := strings.TrimSpace(string(baseOutput))

	headOutput, err := exec.CommandContext(ctx, "git", "-C", absWorktree, "rev-parse", "HEAD").CombinedOutput()
	if err != nil {
		return Diff{}, fmt.Errorf("git rev-parse HEAD failed: %s", strings.TrimSpace(string(headOutput)))
	}
	headCommit := strings.TrimSpace(string(headOutput))

	statOutput, err := exec.CommandContext(ctx, "git", "-C", absWorktree, "diff", "--stat", baseCommit+"..HEAD").CombinedOutput()
	if err != nil {
		return Diff{}, fmt.Errorf("git diff --stat failed: %s", strings.TrimSpace(string(statOutput)))
	}
	patchOutput, err := exec.CommandContext(ctx, "git", "-C", absWorktree, "diff", baseCommit+"..HEAD").CombinedOutput()
	if err != nil {
		return Diff{}, fmt.Errorf("git diff failed: %s", strings.TrimSpace(string(patchOutput)))
	}

	stat := strings.TrimRight(string(statOutput), "\n")
	patch := strings.TrimRight(string(patchOutput), "\n")
	return Diff{
		BaseCommit: baseCommit,
		HeadCommit: headCommit,
		Changed:    stat != "" || patch != "",
		Stat:       stat,
		Patch:      patch,
	}, nil
}

// lockFor 返回指定 repo 的互斥锁，避免同一仓库并发执行 git worktree add 时争抢 git metadata。
func (m *Manager) lockFor(repoPath string) *sync.Mutex {
	value, _ := m.locks.LoadOrStore(repoPath, &sync.Mutex{})
	return value.(*sync.Mutex)
}
