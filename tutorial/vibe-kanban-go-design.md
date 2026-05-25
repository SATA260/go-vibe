# Vibe Kanban Go 版设计文档

> 目标：用 Go + Eino 复刻一个**多 agent 协作 harness**，对齐原项目（Rust 实现）的核心能力，并保留接入进程内 Eino agent 的扩展点。
>
> 阅读对象：要"vibe coding"出 MVP 的实现者。文档按"先骨架后填血肉"的顺序写，不追求一次到位。

---

## 1. 项目定位

vibe-kanban **不是 LLM 框架**，而是**外部 coding agent 的编排器**。它做的事：

1. 把一个任务（task）放进一个 git worktree，调用某个 CLI agent（Claude Code / Codex / Gemini ...）在里面跑
2. 实时捕获 agent 的 stdout/stderr，归一化成结构化事件流，推给前端 kanban UI
3. 把"一次执行"持久化（attempt / execution_process），支持中断后继续、follow-up 对话、PR 创建、review
4. 用一组隔离的 worktree 让多个任务/多个 agent 并行跑互不干扰

Go 版要保留这套定位。"多 agent 协作"在原版里更像是**多种 agent 在不同任务里并行**，而不是同一任务里多 agent 互相对话。我们 MVP 也按前者实现，互相对话作为 Eino 扩展。

### 范围

- **In scope (MVP)**: 单机；本地 git；调用本地 CLI agent；SQLite 持久化；React 前端；SSE/WS 流式
- **Out of scope (MVP)**: 远端部署 / electric sync / OAuth / 团队协作 / 容器 sandbox / MCP 配置注入
- **后续可加**: Eino in-process agent；docker sandbox；远端 worker

---

## 2. 架构总览

```
                 ┌──────────────────────────────────────────────┐
                 │  Frontend (React + TS)                       │
                 │  pages/Tasks  pages/Attempt  pages/Logs      │
                 └───────────┬───────────────────────▲──────────┘
                             │ REST/JSON             │ SSE / WS
                             ▼                       │
┌────────────────────────────────────────────────────┴──────────┐
│  HTTP Server (chi)                                            │
│  routes: /tasks /attempts /executions /events /repos /config  │
└────────────┬──────────────────────┬───────────────┬───────────┘
             │                      │               │
             ▼                      ▼               ▼
      ┌──────────────┐      ┌──────────────┐  ┌──────────────┐
      │  Container   │      │   Events     │  │  Approvals   │
      │  Service     │◄────►│   Service    │  │  Service     │
      │ (orchestrator│      │ (DB hooks +  │  │ (tool perm   │
      │  of attempts)│      │  MsgStore)   │  │  prompts)    │
      └──┬───────┬───┘      └──────┬───────┘  └──────────────┘
         │       │                 │
         ▼       ▼                 ▼
   ┌─────────┐ ┌──────────────┐  ┌──────────────────────────┐
   │ Worktree│ │  Executor    │  │  MsgStore registry       │
   │ Manager │ │  Registry    │  │  exec_id → broadcast hub │
   └─────────┘ └──┬───────────┘  └──────────────────────────┘
                  │
                  ▼  (impls)
       ┌──────────────────────────────────────┐
       │ ClaudeCode  Codex  Gemini  Cursor    │
       │ Opencode    Amp    Copilot ... Eino  │
       │ (each: spawn + log normalize)        │
       └──────────────────────────────────────┘

┌────────────────────────────────────────────────────────────────┐
│  Storage:  SQLite (sqlc)  +  ./worktrees/  +  ./logs/          │
└────────────────────────────────────────────────────────────────┘
```

### 数据流（一次 attempt 的生命周期）

1. 用户在 UI 创建 task → POST `/tasks`
2. 用户点 Start → POST `/attempts`，body 含 `executor_id` 和 `executor_config`
3. `ContainerService` 创建 worktree（`WorktreeManager`），落库 `ExecutionProcess(status=running)`
4. 给该 process 建 `MsgStore`，注册到 registry
5. 通过 `Executor.Spawn(ctx, dir, prompt, env)` 拉起子进程，得到 stdout/stderr pipe
6. 一个 goroutine 把原始字节按行 push 进 `MsgStore`（`LogStdout`/`LogStderr`）
7. `Executor.NormalizeLogs(store, worktree)` 启动若干解析 goroutine：消费原始消息，发出 `LogJSONPatch`（结构化条目），还可能 push `SessionID`/`MessageID`
8. 前端 GET `/events/:exec_id` 走 SSE，从 `MsgStore.HistoryPlusStream` 拿到全量历史 + 实时增量
9. 进程退出 → push `LogFinished`，更新 DB 状态；如果 action 链表里有 `next_action`，自动起下一段（例如 setup script → coding agent → cleanup script）
10. follow-up：用户再发 prompt → 同一个 attempt 起新的 ExecutionProcess，传 session_id 续上

---

## 3. 仓库布局

参考原项目按"crate"切的结构，Go 版按 `internal/` 子包切。模块边界跟原项目一致，名字本地化。

```
vibe-kanban-go/
├── cmd/
│   ├── server/           # 主入口：启动 HTTP + DB + worktree manager
│   └── generate-types/   # 从 Go struct 生成 frontend types（用 tygo 或 ts-codegen）
├── internal/
│   ├── apitypes/         # 共享 API 类型；前端通过生成器拿到 .ts
│   ├── db/
│   │   ├── migrations/   # 编号 SQL，golang-migrate
│   │   ├── queries/      # sqlc *.sql
│   │   └── models/       # sqlc 生成的 + 手写补充
│   ├── git/              # 封装 git CLI 调用（go-git 不够用就 shell out）
│   ├── worktree/         # WorktreeManager：创建/复用/清理
│   ├── executors/        # Executor 接口 + 各 agent 实现
│   │   ├── claude/
│   │   ├── codex/
│   │   ├── gemini/
│   │   ├── cursor/
│   │   ├── eino/         # 进程内 Eino agent（后续）
│   │   ├── action.go     # ExecutorAction 链表
│   │   ├── env.go        # ExecutionEnv
│   │   └── registry.go
│   ├── logs/             # NormalizedEntry 类型 + json patch 工具
│   ├── msgstore/         # MsgStore + LogMsg
│   ├── services/
│   │   ├── container/    # ContainerService：编排 attempt 生命周期
│   │   ├── events/       # EventService：DB 变更 → patch
│   │   ├── approvals/    # 工具调用授权
│   │   ├── pr/           # PR monitor（可选）
│   │   └── notification/
│   ├── server/
│   │   ├── routes/       # 一个文件一个路由族，对齐原 routes/
│   │   ├── middleware/
│   │   └── sse.go
│   ├── runtime/          # Deployment 抽象：local-deployment 等价物
│   └── config/           # 用户配置文件读写
├── web/                  # 前端，沿用 React + Vite + Tailwind
│   ├── src/
│   │   ├── pages/
│   │   ├── features/
│   │   └── shared/types.ts  # 自动生成
│   └── ...
├── shared/               # 自动生成的 TS 类型
├── scripts/              # dev 启动、port 协调
└── go.mod
```

---

## 4. 核心抽象

### 4.1 `Executor` 接口

对齐 Rust 的 `StandardCodingAgentExecutor`。

```go
package executors

import (
    "context"
    "io"
    "path/filepath"
)

// Spawned 一次子进程的句柄。Wait 返回退出码或错误。
type Spawned struct {
    Stdout io.ReadCloser
    Stderr io.ReadCloser
    Stdin  io.WriteCloser // 可选；某些 agent 走 stdin 续 prompt
    Wait   func() (int, error)
    Kill   func() error
    Pid    int
}

type Executor interface {
    // ID 例如 "CLAUDE_CODE" / "CODEX" / "EINO"
    ID() string

    // Spawn 起一个全新会话
    Spawn(ctx context.Context, dir string, prompt string, env *ExecutionEnv) (*Spawned, error)

    // SpawnFollowUp 续会话；resetTo 可选，回滚到某条 message
    SpawnFollowUp(
        ctx context.Context,
        dir, prompt, sessionID string,
        resetTo *string,
        env *ExecutionEnv,
    ) (*Spawned, error)

    // NormalizeLogs 启动解析 goroutine，把 raw stdout/stderr → JsonPatch / SessionID / MessageID
    // 返回的 cancel 调用时停止所有解析
    NormalizeLogs(store *msgstore.MsgStore, worktree string) (cancel func())

    // Capabilities 描述支持的能力，例如 SessionFork / ContextUsage
    Capabilities() []Capability

    // Availability 探测本机有没有装、有没有登录
    Availability(ctx context.Context) Availability
}

type Capability string

const (
    CapSessionFork  Capability = "SESSION_FORK"
    CapContextUsage Capability = "CONTEXT_USAGE"
    CapSetupHelper  Capability = "SETUP_HELPER"
)

type Availability struct {
    Installed bool
    LoggedIn  bool
    Detail    string // 给 UI 显示
}
```

### 4.2 `ExecutorAction` 链表

`Action.Next` 让我们把 setup → agent → cleanup 串成一条链，原项目里就是这么做的。

```go
type ActionType string

const (
    ActionCodingAgentInitial   ActionType = "CodingAgentInitialRequest"
    ActionCodingAgentFollowUp  ActionType = "CodingAgentFollowUpRequest"
    ActionScript               ActionType = "ScriptRequest"
    ActionReview               ActionType = "ReviewRequest"
)

type Action struct {
    Type ActionType      `json:"type"`
    Body json.RawMessage `json:"body"`     // 按 Type 解
    Next *Action         `json:"next,omitempty"`
}

// 具体 body
type CodingAgentInitial struct {
    Prompt         string         `json:"prompt"`
    ExecutorConfig ExecutorConfig `json:"executor_config"`
    WorkingDir     *string        `json:"working_dir,omitempty"`
}
type ScriptRequest struct {
    Language string   `json:"language"` // bash / sh / node
    Script   string   `json:"script"`
    Context  string   `json:"context"`  // setup / cleanup / dev
}
```

`Executable` 接口（每种 body 实现）：

```go
type Executable interface {
    Run(ctx context.Context, dir string, env *ExecutionEnv) (*Spawned, error)
}
```

### 4.3 `MsgStore` + `LogMsg`

完全照搬原项目语义：内存里维护一个有上限的历史 ring，加 broadcast channel 给实时订阅者。

```go
package msgstore

type LogKind string

const (
    KindStdout    LogKind = "stdout"
    KindStderr    LogKind = "stderr"
    KindJSONPatch LogKind = "json_patch"
    KindSessionID LogKind = "session_id"
    KindMessageID LogKind = "message_id"
    KindReady     LogKind = "ready"
    KindFinished  LogKind = "finished"
)

type LogMsg struct {
    Kind LogKind         `json:"kind"`
    Data json.RawMessage `json:"data"`
}

type MsgStore struct {
    mu       sync.RWMutex
    history  []LogMsg
    bytes    int
    maxBytes int          // 默认 8MB
    subs     map[uint64]chan LogMsg
    nextSub  uint64
}

func (s *MsgStore) Push(msg LogMsg)
func (s *MsgStore) PushStdout(line string)
func (s *MsgStore) PushPatch(patch jsonpatch.Patch)
func (s *MsgStore) PushFinished()
func (s *MsgStore) Subscribe() (history []LogMsg, ch <-chan LogMsg, cancel func())
// 等价于 history_plus_stream
func (s *MsgStore) HistoryPlusStream(ctx context.Context) <-chan LogMsg
```

`json_patch` 用 `github.com/evanphx/json-patch/v5`。前端在内存里维护一个 conversation 文档，按 patch 增量更新即可——这是原项目最聪明的部分，保留。

`NormalizedEntry` 形状：

```go
type NormalizedEntry struct {
    Timestamp int64        `json:"ts"`
    Type      EntryType    `json:"type"` // user_message / assistant_message / tool_use / tool_result / thinking / system
    Content   any          `json:"content"`
    ToolName  string       `json:"tool_name,omitempty"`
    Metadata  map[string]any `json:"metadata,omitempty"`
}
```

每个 executor 的 `NormalizeLogs` 就是把 agent 私有的 JSON/文本流读出来，构造 `NormalizedEntry`，再发 add patch。

### 4.4 `WorktreeManager`

```go
package worktree

type Manager struct {
    rootDir string // ./worktrees/
    locks   sync.Map // repoPath -> *sync.Mutex，避免并发 git worktree add
}

type Cleanup struct {
    WorktreePath string
    GitRepoPath  string
}

func (m *Manager) Create(ctx context.Context, repoPath, branchName, worktreePath, baseBranch string, createBranch bool) error
func (m *Manager) Remove(ctx context.Context, c Cleanup) error
func (m *Manager) EnsureExists(ctx context.Context, repoPath, worktreePath, branchName string) error
```

实现：直接 `exec.CommandContext("git", "worktree", "add", ...)`。go-git 对 worktree 支持不好，shell out 最稳。

### 4.5 `ContainerService`（编排核心）

类比原项目 `services/container.rs`。一个 attempt 的状态机：

```
idle → running → finished/failed/killed
                ↘ has_next_action → spawn next process → running ...
```

```go
type ContainerService struct {
    db        *db.Queries
    wt        *worktree.Manager
    registry  *executors.Registry
    msgs      *msgstore.Registry  // exec_id → *MsgStore
    events    *events.Service
    approvals *approvals.Service
}

func (c *ContainerService) StartAttempt(ctx context.Context, req StartReq) (AttemptID, error)
func (c *ContainerService) FollowUp(ctx context.Context, attemptID AttemptID, prompt string) error
func (c *ContainerService) Stop(ctx context.Context, execID ExecID) error
func (c *ContainerService) ResumeOnBoot(ctx context.Context) error // 启动时把 status=running 的清理或恢复
```

`StartAttempt` 内部：
1. 在 `ContainerService.mu` 下创建 worktree（`WorktreeManager` 自己也加锁，幂等）
2. 落库 `Workspace`（worktree 元数据）+ `Session` + 第一条 `ExecutionProcess`
3. 起 goroutine `runAction(ctx, action)`，里面循环：spawn → 等 wait → 推 finished → 看 next_action

---

## 5. 数据模型

照原项目的核心表精简，先做 MVP 必需的。

```sql
-- repos：用户在本地登记的仓库
CREATE TABLE repos (
    id            TEXT PRIMARY KEY,           -- uuid
    name          TEXT NOT NULL,
    git_repo_path TEXT NOT NULL,
    default_target_branch TEXT NOT NULL DEFAULT 'main',
    setup_script   TEXT,
    cleanup_script TEXT,
    dev_script     TEXT,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL
);

-- tasks：kanban 卡片
CREATE TABLE tasks (
    id          TEXT PRIMARY KEY,
    repo_id     TEXT NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    title       TEXT NOT NULL,
    description TEXT,
    status      TEXT NOT NULL,               -- todo/in_progress/in_review/done/cancelled
    parent_task_id TEXT REFERENCES tasks(id),
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL
);

-- workspaces：等价于原 task_attempts，跟一个 worktree 绑定
CREATE TABLE workspaces (
    id            TEXT PRIMARY KEY,
    task_id       TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    repo_id       TEXT NOT NULL REFERENCES repos(id),
    branch        TEXT NOT NULL,
    base_branch   TEXT NOT NULL,
    container_ref TEXT NOT NULL,             -- worktree 路径
    worktree_deleted BOOLEAN NOT NULL DEFAULT 0,
    created_at TIMESTAMP NOT NULL
);

-- sessions：一段连续会话（允许 fork）
CREATE TABLE sessions (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name         TEXT,
    executor_id  TEXT NOT NULL,              -- CLAUDE_CODE / ...
    executor_config JSON NOT NULL,
    agent_session_id TEXT,                   -- agent 自己的 sessionId（resume 用）
    agent_working_dir TEXT,
    created_at TIMESTAMP NOT NULL
);

-- execution_processes：每次跑的子进程
CREATE TABLE execution_processes (
    id            TEXT PRIMARY KEY,
    session_id    TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    workspace_id  TEXT NOT NULL,
    run_reason    TEXT NOT NULL,             -- setup/coding_agent/cleanup/dev/review
    executor_id   TEXT NOT NULL,
    executor_action JSON NOT NULL,           -- 整个 Action 链表 JSON
    status        TEXT NOT NULL,             -- running/completed/failed/killed
    exit_code     INTEGER,
    pid           INTEGER,
    started_at    TIMESTAMP NOT NULL,
    finished_at   TIMESTAMP,
    before_head_commit TEXT,
    after_head_commit  TEXT,
    masked_by_restore  BOOLEAN NOT NULL DEFAULT 0
);

-- execution_process_logs：归一化 entry，按 batch 落
CREATE TABLE execution_process_logs (
    execution_process_id TEXT NOT NULL,
    seq                  INTEGER NOT NULL,
    entry                JSON NOT NULL,
    created_at TIMESTAMP NOT NULL,
    PRIMARY KEY (execution_process_id, seq)
);

CREATE INDEX idx_exec_proc_session ON execution_processes(session_id);
CREATE INDEX idx_workspaces_task   ON workspaces(task_id);
```

后续再加 `merges`、`tracked_prs`、`approvals`、`coding_agent_turns`，照原项目 migration 抄即可。

---

## 6. 流式协议（前端 ↔ 后端）

### 6.1 SSE 端点

`GET /api/events/execution-processes/:exec_id`

事件类型：

| event       | data                                | 说明 |
|-------------|-------------------------------------|------|
| `stdout`    | string（一行）                       | 原始 |
| `stderr`    | string                              | 原始 |
| `json_patch`| `[{op,path,value}, ...]`            | 对该 exec 的归一化 conversation 文档打 patch |
| `session_id`| string                              | agent 自己的 session id |
| `message_id`| string                              | 当前正在生成的 assistant message id |
| `ready`     | (空)                                | 可以开始接收了 |
| `finished`  | (空)                                | 进程结束 |

历史回放：subscribe 时先把 `MsgStore.history` 全部按上面格式发一遍，再切到 live。前端无感。

### 6.2 DB 变更广播

仿原项目 `EventService`：用 SQLite 的 update hook 把 INSERT/UPDATE/DELETE 转成对 `tasks` / `workspaces` / `execution_processes` 的 JSON patch，走 `GET /api/events/records` SSE。前端的 list 页面用同一套 patch 协议刷新。

> Go 里可以用 `mattn/go-sqlite3` 的 `RegisterPreUpdateHook`。或者更简单：service 层每次写完手动发一份 patch。

---

## 7. HTTP API 表面（MVP）

```
GET  /api/health
GET  /api/config              # 全局配置（默认 executor、quiet hours 等）
PUT  /api/config

GET    /api/repos
POST   /api/repos
GET    /api/repos/:id
DELETE /api/repos/:id

GET    /api/tasks?repo_id=
POST   /api/tasks
PATCH  /api/tasks/:id
DELETE /api/tasks/:id

POST   /api/tasks/:id/workspaces             # = 启动一个 attempt
GET    /api/workspaces/:id
GET    /api/workspaces/:id/diff              # worktree vs base_branch
POST   /api/workspaces/:id/follow-up
POST   /api/workspaces/:id/stop
POST   /api/workspaces/:id/merge             # git merge / 创建 PR

GET    /api/execution-processes/:id
GET    /api/execution-processes/:id/logs     # 归一化 conversation 全量
GET    /api/events/execution-processes/:id   # SSE
GET    /api/events/records                   # SSE，DB 变更

GET    /api/executors                        # 列出可用 + availability
POST   /api/approvals/:id/respond
```

---

## 8. Eino 接入点

把 Eino-backed agent 实现成**普通的 `Executor`**，跟 ClaudeCode 平级。这是最干净的隔离。

```go
package eino

type Executor struct {
    chain compose.Runnable[Input, Output] // 用 eino compose 组装的 ReAct 或 graph
}

func (e *Executor) Spawn(ctx context.Context, dir, prompt string, env *ExecutionEnv) (*Spawned, error) {
    // 不起子进程；起一个 goroutine 跑 e.chain.Stream(ctx, ...)
    // 把 stream 输出适配成 stdout pipe
    pr, pw := io.Pipe()
    go func() {
        defer pw.Close()
        stream, _ := e.chain.Stream(ctx, Input{Prompt: prompt, WorkDir: dir})
        for chunk := range stream { ... write JSON 到 pw ... }
    }()
    return &Spawned{Stdout: pr, Wait: ..., Kill: ...}, nil
}

func (e *Executor) NormalizeLogs(store *msgstore.MsgStore, _ string) func() {
    // 因为 Spawn 阶段已经写的是结构化 JSON，这里就是 JSON → NormalizedEntry → patch
}
```

要点：
- **Tool 执行**：Eino 的 tool 在我们这层包一层，注入 worktree 路径限制 + approval 检查
- **Sandbox**：MVP 不做；后续在 tool wrapper 里加 docker exec / chroot
- **Context 压缩**：在 ChatModel 调用前的 middleware 里做，跟 Eino 解耦
- **持久化**：`agent_session_id` 字段存 Eino 自己的 thread id（如果用了 ADK 的 session）

什么时候动 Eino 源码？只有在需要 Graph 执行器暴露中间 state checkpoint hook 时。其他都是上层包装。

---

## 9. 技术选型

| 模块 | 选型 | 理由 |
|------|------|------|
| HTTP | `chi` | 轻、跟 net/http 兼容、middleware 生态够 |
| SSE  | 自己写 + `chi` | 一百来行 |
| WS（terminal/ssh）| `coder/websocket` | 比 gorilla 维护好 |
| DB | SQLite + `mattn/go-sqlite3` + `sqlc` | 跟原项目同构；sqlc 生成类型安全 query |
| Migration | `golang-migrate/migrate` | 标准 |
| JSON Patch | `evanphx/json-patch/v5` | RFC 6902，跟前端一致 |
| Git | shell out `git` CLI | go-git 不支持 worktree |
| 进程管理 | `os/exec` + `process group` | Linux 用 `Setpgid`，kill 进程组 |
| Logging | `slog` | 标准库 |
| 配置 | `koanf` 或自己写 | toml/json |
| TS 类型生成 | `tygo` | 简单够用；复杂 sum type 可能需要手补 |
| Frontend | 复用原项目的 React + Vite + Tailwind | 接口对齐就能直接搬 |
| Agent loop（可选） | `cloudwego/eino` | 进程内 agent |

---

## 10. 里程碑

按"能跑通最小闭环"切，每个里程碑独立可验证。

### M0 · 骨架（半天）
- `cmd/server` 启动 chi + sqlite，迁移跑通
- `/api/health` 返回 ok
- 前端起一个空壳页面

### M1 · 一个 task 跑通一个 echo
- `repos / tasks / workspaces / execution_processes` 表
- `WorktreeManager.Create` 能在 `./worktrees/` 下建出来
- 一个 `EchoExecutor`：`Spawn` 就是 `bash -c "echo hello && sleep 2 && echo done"`
- `MsgStore` + SSE 前端能看到 stdout 实时滚出来
- Stop 能 kill 进程

### M2 · 接第一个真 agent（Claude Code）
- 写 `executors/claude/`：spawn 命令 = `claude -p "..." --output-format=stream-json --verbose`
- `NormalizeLogs` 解析 `--output-format=stream-json` 那种 NDJSON，发 JSON patch
- 前端实现 conversation 视图（user → assistant → tool_use → tool_result）

### M3 · Action 链 + follow-up
- `Action.Next` 串 setup_script → coding_agent → cleanup_script
- follow-up：传 `agent_session_id`，复用 worktree
- Diff 视图：`git diff base_branch...HEAD` 走 API

### M4 · 第二个 agent + executor registry
- 加 Codex 或 Gemini 之一
- `/api/executors` 返回 availability，UI 选择器联通
- 解决一个"两个 agent 行为差异"的 corner case，验证抽象到位

### M5 · Eino executor
- `executors/eino/` 跑通一个最小 ReAct（read_file / write_file / run_bash）
- Tool wrapper 加 worktree boundary 检查
- 在同一 task 里可切换"Claude vs Eino"对比

后面再做：approvals、PR 创建、multi-agent 协作（同一 attempt 里 planner→coder→reviewer 串）、远端部署。

---

## 11. 关键决策与坑

- **不要把 Eino 当主框架**。Eino 是 ChatModel + Tool + Graph 的库，harness 层的 worktree / 持久化 / SSE / hook 不是 Eino 的事。Eino 只是一种 Executor。
- **JSON patch 协议不要改**。前端一旦实现"对文档打 patch"的 reducer，所有 agent 共用。每个 agent 的差异封死在 `NormalizeLogs` 里。
- **进程组 kill 是必须的**。CLI agent 经常自己 spawn 子进程，普通 `Process.Kill` 留僵尸。Linux 用 `Setpgid: true` + `Kill -pgid`。
- **worktree 创建必须串行化**（按 repo path 加锁），否则 git index lock 冲突。原项目就这么做。
- **MsgStore 历史有上限**。默认 8MB ring，超过丢最旧。前端要能容忍"打开太晚就看不到全部 stdout"——只看 normalized conversation 是完整的（因为它在 DB 里）。
- **DB 写要走 service 层**，不要让 route handler 直接写 sqlc。否则 events 广播容易漏发。
- **sqlc 跟 SQLite 的 JSON 列**：用 `text` + 自定义 scanner 把 `[]byte` 反序列化成 `Action` / `ExecutorConfig`。
- **ts 类型生成**：tygo 对 tagged union 不友好；`Action.Type + Body json.RawMessage` 这种结构在 ts 端用 `type Action = { type: 'X', body: XBody } | ...` 手动写一份 union，或者用 `oapi-codegen` 走 OpenAPI。MVP 直接手写 union 最快。
- **Approval 机制不要先做**。先让 agent 全自动跑，等真的需要"危险命令拦截"时再加；提前做的 approval 抽象 80% 概率会重写。

---

## 12. 不做什么

- 不做远端部署 / electric sync（原项目 `crates/remote`）
- 不做 OAuth / 团队（原项目 `crates/relay-*`）
- 不做 desktop 打包（Tauri）
- 不做 MCP server 配置注入——这是 agent 自己的事，让用户自己配

这些不是技术上做不了，是 MVP 不需要。先把单机闭环打磨到顺手再说。

---

## 附录 A · Spawn 子进程的标准模板

```go
func spawnCmd(ctx context.Context, dir string, name string, args []string, env []string) (*Spawned, error) {
    cmd := exec.CommandContext(ctx, name, args...)
    cmd.Dir = dir
    cmd.Env = append(os.Environ(), env...)
    cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // Linux/macOS

    stdout, err := cmd.StdoutPipe(); if err != nil { return nil, err }
    stderr, err := cmd.StderrPipe(); if err != nil { return nil, err }
    stdin,  err := cmd.StdinPipe();  if err != nil { return nil, err }
    if err := cmd.Start(); err != nil { return nil, err }

    return &Spawned{
        Stdout: stdout, Stderr: stderr, Stdin: stdin, Pid: cmd.Process.Pid,
        Wait: func() (int, error) {
            err := cmd.Wait()
            if exitErr, ok := err.(*exec.ExitError); ok { return exitErr.ExitCode(), nil }
            if err != nil { return -1, err }
            return 0, nil
        },
        Kill: func() error {
            if cmd.Process == nil { return nil }
            return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) // 整个进程组
        },
    }, nil
}
```

## 附录 B · `MsgStore.HistoryPlusStream` 实现要点

```go
func (s *MsgStore) HistoryPlusStream(ctx context.Context) <-chan LogMsg {
    out := make(chan LogMsg, 64)
    s.mu.Lock()
    hist := append([]LogMsg(nil), s.history...)
    id := s.nextSub; s.nextSub++
    sub := make(chan LogMsg, 256)
    s.subs[id] = sub
    s.mu.Unlock()

    go func() {
        defer close(out)
        defer func() {
            s.mu.Lock(); delete(s.subs, id); s.mu.Unlock()
        }()
        for _, m := range hist {
            select { case out <- m: case <-ctx.Done(): return }
        }
        for {
            select {
            case m, ok := <-sub:
                if !ok { return }
                select { case out <- m: case <-ctx.Done(): return }
            case <-ctx.Done(): return
            }
        }
    }()
    return out
}
```

注意：history 和 subscribe 之间必须**原子获取**，否则会丢消息——`Push` 时持锁同时 append history 并 fanout 到所有 subs，`HistoryPlusStream` 拿 history 的同时注册 sub，两者同一把锁。

## 附录 C · 前端 conversation reducer 草图

```ts
type Doc = { entries: NormalizedEntry[] }

function reduce(doc: Doc, msg: LogMsg): Doc {
  if (msg.kind === 'json_patch') {
    return applyPatch(doc, msg.data) // RFC 6902
  }
  return doc
}
```

后端发的 patch 形如 `[{op:'add', path:'/entries/-', value: {...}}]`、`[{op:'replace', path:'/entries/3/content', value: '...'}]`。这套机制让"流式输出 token-by-token 拼装到一条 assistant message"变得很自然——后端持续 replace 同一条 entry 的 content。

---

文档到此。下一步：按 M0 起骨架，跑通 hello world 后再回头细化 M1。遇到 Rust 行为不确定的地方，直接读 [`crates/services/src/services/container.rs`](../crates/services/src/services/container.rs) 和 [`crates/executors/src/executors/mod.rs`](../crates/executors/src/executors/mod.rs)——这两个文件是整个 harness 的脊柱。
