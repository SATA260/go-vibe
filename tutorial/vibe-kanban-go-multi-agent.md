# 多 Agent 协作详细方案

> 配套文档：[vibe-kanban-go-design.md](vibe-kanban-go-design.md)
>
> 这份文档单独讲清楚"多 agent 协作"在 Go 版 harness 里到底是什么、怎么落地、跟 Eino 的关系。

---

## 1. 先把"多 agent"分清楚

"多 agent 协作"在不同语境下完全是两件事，混在一起讨论一定会绕。我们先切开：

| 层次          | 谁在协作                              | 跑在哪                | 例子 |
|---------------|---------------------------------------|-----------------------|------|
| **L1 进程级** | 多个独立 Executor 进程在同一 task 上接力 | 各自子进程 / Eino goroutine | planner agent → coder agent → reviewer agent |
| **L2 进程内** | 一个 Eino agent **内部**有多个子 agent | 同一个 Go 进程        | Eino MultiAgent（supervisor + workers）作为单个 Executor |
| **L3 子调度** | agent 在自己跑的过程中拉起一个临时子 agent | 由 agent 自己控制     | Claude 的 `Agent` tool；Codex 的 task 派生 |

vibe-kanban 现有的 `parent_task_id` 是 **L3 的副产品**——Claude 解析自己 stdout 的 Task tool result，把 subagent 输出落到 DB 里，但这个 subagent 是 Claude 自己内部跑的，harness 只是观察者。

我们 Go 版要做的是 **L1 主导，L2 作为单 Executor 的内部细节，L3 透传**。原因：

- **L1 是 harness 真正的价值**：在它这一层我们能控制 worktree、approval、checkpoint，能换 agent，能并行
- **L2 是 Eino 的事**：你用 Eino 写一个 supervisor agent，它对 harness 来说就是一个 Executor，没必要把 supervisor 抽象漏到 harness 层
- **L3 不是我们能干涉的**：CLI agent 自己派生的 subagent 我们看不到内部结构，能做的只是在 normalized log 里渲染出来

下面所有"多 agent 协作"如果不另外说，都指 **L1**。

---

## 2. 三种 L1 协作模式

### 2.1 Pipeline（顺序接力）

```
[planner]  →  [coder]  →  [reviewer]
   ↑              ↑              ↑
   ExecutorAction.Next 链表
```

**共享什么**：同一个 workspace（同一个 worktree、同一个 branch）。后一个 agent 看到前一个的代码改动 = git diff。

**怎么传 context**：
- 简单做法：直接把上一个 agent 的最后 N 条 normalized entry 序列化成 markdown，塞进下一个 agent 的 prompt
- 更结构化：在 worktree 里写一个 `.vk/handoff.md`，每个 agent 进来先读它、跑完更新它

**对应原项目机制**：现成的 `ExecutorAction.Next`。每段是独立 ExecutionProcess，独立 session，独立 normalized conversation。前端展示成"step 1 / step 2 / step 3" tab。

**这是 MVP 唯一要做的多 agent 模式。** 其他两种是后续。

### 2.2 Parallel + Merge（并行竞标）

```
                 [coder-claude]   ──┐
                                    │
[task] ──fan-out──[coder-codex]   ──┼──merge──→ [reviewer/选择]
                                    │
                 [coder-eino]     ──┘
   各自独立 worktree，独立 branch
```

**共享什么**：什么都不共享。每个 agent 在自己的 worktree。

**怎么聚合**：
- 全跑完 → 让一个 reviewer agent 看三个 git diff 选最好的 / 合并
- 或者人工挑

**用途**：同一个需求让几个 agent 各做一遍对比；或者让 Claude 实现 + Codex 写测试 + Gemini 写文档，各干各的最后合 diff。

**实现关键**：需要新加一个 action type `ParallelGroup`，和一个"等待所有子 attempt 完成"的同步点 action。详见 §4。

### 2.3 Iterative review（同伴互审）

```
[coder] → [reviewer] → 通过？
            │
            └─── 否 ──→ [coder follow-up with feedback] → [reviewer] → ...
```

**共享什么**：同一 workspace；reviewer 只读，coder 可写。

**关键**：循环要有终止条件——最多 N 轮，或者 reviewer 输出特定 token（"APPROVED"），或者 coder 跑了但没改文件。

**实现关键**：需要在 action 链里支持条件跳转，不只是线性 next。可以做成"meta executor"：起一个 ConductorExecutor，它不真跑 LLM，而是在 harness 内部循环 spawn coder/reviewer。

> Iterative 模式很容易跑飞（无限循环、token 烧光）。MVP 不做。等 Pipeline 跑顺了，确认有真实 use case 再加。

---

## 3. 核心抽象扩展

在 §4 的 `Executor` / `Action` 之上加最小增量。

### 3.1 `Action` 增加两种类型

```go
const (
    ActionCodingAgentInitial  ActionType = "CodingAgentInitialRequest"
    ActionCodingAgentFollowUp ActionType = "CodingAgentFollowUpRequest"
    ActionScript              ActionType = "ScriptRequest"
    ActionReview              ActionType = "ReviewRequest"

    // 新增
    ActionParallelGroup   ActionType = "ParallelGroupRequest"   // 见 §2.2
    ActionHandoff         ActionType = "HandoffRequest"         // 拼上一段输出 → 下一段 prompt
)

type ParallelGroupRequest struct {
    // 每个 branch 自己的子 action 链（通常是一个 CodingAgentInitialRequest）
    Branches []Action `json:"branches"`
    // 怎么合：none / pick_first_success / reviewer_action
    Merge MergeStrategy `json:"merge"`
    // reviewer_action 时填，是一个 Action
    ReviewerAction *Action `json:"reviewer_action,omitempty"`
}

type HandoffRequest struct {
    // 把上一段的 normalized conversation 转成 prompt 模板，喂给下一段
    Template string `json:"template"`        // 例如 "前一个 agent 的总结：\n\n{{.PreviousSummary}}\n\n请你..."
    PickFrom HandoffSource `json:"pick_from"` // last_assistant_message / full_conversation / git_diff
}
```

`HandoffRequest` 不真起子进程，它在 harness 内部把上一个 ExecutionProcess 的 normalized log 取出来，按 template 渲染出新 prompt，**改写**链表里的下一个 `CodingAgentInitialRequest.Prompt`，然后继续推进。

> 设计取舍：也可以让每个 agent 自己读 `.vk/handoff.md`，不走 harness。但走 harness 的好处是前端能渲染"上下文是怎么从 A 传到 B 的"，可观测性更好。

### 3.2 Workspace 关系

新增 `workspace_kind`：

```sql
ALTER TABLE workspaces ADD COLUMN kind TEXT NOT NULL DEFAULT 'primary';
-- 'primary' | 'parallel_branch'
ALTER TABLE workspaces ADD COLUMN parent_workspace_id TEXT REFERENCES workspaces(id);
ALTER TABLE workspaces ADD COLUMN parallel_group_id TEXT;
```

Pipeline 不需要这些字段（同一个 workspace）。Parallel 需要：每个 branch 一个 workspace，共同 parent，相同 group id。

### 3.3 `ConductorService`

把"跑一条 action 链"的逻辑抽出来，叫 `Conductor`。`ContainerService` 负责创建 workspace，`Conductor` 负责按 action 链推进。这层在原项目里其实是混在 container 里的；Go 版分开能让多 agent 逻辑放在 `Conductor` 里不污染 container。

```go
type Conductor struct {
    db        *db.Queries
    wt        *worktree.Manager
    registry  *executors.Registry
    msgs      *msgstore.Registry
}

// 推进一条 action 链；返回 chan，每个元素是一段 ExecutionProcess 的退出事件
func (c *Conductor) Run(ctx context.Context, sessionID SessionID, root Action) <-chan StageEvent

type StageEvent struct {
    ExecID   ExecID
    Stage    int       // 第几段
    Status   string    // running / completed / failed
    NextID   *ExecID   // 下一段如果起了
}
```

内部按 action 类型分发：
- `CodingAgentInitial / FollowUp / Script / Review`：起子进程，等结束
- `Handoff`：合成 prompt，推进
- `ParallelGroup`：扇出 N 个子 Conductor.Run，全完成或第一个成功（按 strategy），再走 ReviewerAction（如果有）

---

## 4. 并行与合并实现细节

并行是这套方案最容易翻车的地方。挑几个真问题讲清楚。

### 4.1 worktree 创建并发

`WorktreeManager` 已经按 repo path 加锁。并行 N 个 branch → N 次 `git worktree add`，串行执行（在锁里），每个 ~100ms 不是瓶颈。**保留这个锁，不要为了并行优化它**——git index 真的会冲突。

### 4.2 branch 命名

```
{task-slug}-{group-id-short}-{branch-name}
例如：fix-login-a3f-claude / fix-login-a3f-codex / fix-login-a3f-eino
```

group-id 是 `parallel_group_id`，branch-name 由前端传或者用 executor id。

### 4.3 ParallelGroup 完成判定

```go
type MergeStrategy string
const (
    MergeNone           MergeStrategy = "none"            // 全跑完，留给用户挑
    MergeFirstSuccess   MergeStrategy = "first_success"   // 第一个 exit 0 就 cancel 其余
    MergeReviewer       MergeStrategy = "reviewer"        // 全跑完 → 起 reviewer
)
```

- `none` 最简单，先做这个
- `first_success` 要小心：cancel 其他时 worktree 留着不删，让用户能事后看
- `reviewer` 复杂在"reviewer 输入是什么"——把 N 个 branch 的 git diff 拼起来塞 prompt，超长是常态。MVP 不做

### 4.4 reviewer action 的 prompt 注入

reviewer 不是普通 CodingAgentInitial，它需要看到 N 个 branch 的产出。Conductor 在拉起 reviewer 前：
1. 对每个 branch 跑 `git diff base...branch`
2. 渲染成 markdown：

```
# Branch 1: claude
{diff}

# Branch 2: codex
{diff}

...
```

3. 把 reviewer action 的 prompt 替换成"请审阅以下 N 个实现，选出最佳的或合并它们……" + 上面的 markdown
4. reviewer 跑在哪个 worktree？建一个临时 review worktree，base = task base branch，让它能 cherry-pick / merge

### 4.5 取消语义

并行组里某个 branch 失败 / 被用户 stop，其他怎么办？

- 默认：互不影响，各跑各的。group 整体状态由 strategy 决定
- `first_success` 例外：第一个成功后给其他发 SIGTERM
- `MergeReviewer`：任意一个失败 → group 失败，其他继续跑完但不进 reviewer（让用户决定要不要重试）

---

## 5. Eino 在哪一层进场

回到一开始的问题：Eino 二开值不值。在多 agent 场景下答案更清楚：

### 5.1 用 Eino 实现一个 Executor（推荐路径）

把 Eino MultiAgent / Graph 包成一个 `EinoSupervisorExecutor`：

```go
type EinoSupervisorExecutor struct {
    runnable compose.Runnable[Input, Output]  // 内部是 supervisor + worker 的 graph
    tools    []tool.BaseTool                  // read_file / edit_file / run_bash
}

func (e *EinoSupervisorExecutor) Spawn(ctx, dir, prompt, env) (*Spawned, error) {
    pr, pw := io.Pipe()
    go func() {
        defer pw.Close()
        stream, _ := e.runnable.Stream(ctx, Input{Prompt: prompt, Dir: dir})
        for chunk := range stream {
            // 把 Eino 的 message chunk 编码成跟 Claude stream-json 类似的 NDJSON
            // 这样 NormalizeLogs 能复用解析器
            json.NewEncoder(pw).Encode(toStreamEvent(chunk))
        }
    }()
    return &Spawned{Stdout: pr, Wait: ..., Kill: ...}, nil
}
```

好处：
- harness 完全不知道里面有几个 agent，只看到一个 ExecutorAction
- 所有 worktree / approval / 持久化机制不用动
- 想换 supervisor 实现就只改这个 Executor

代价：
- Eino 的 multi-agent 内部决策（哪个 worker 跑、跑了几次）harness 看不到，前端也展示不了细节
- 如果用户想"在 worker 失败时人工干预"，做不到——这是 Eino 内部循环

### 5.2 用 harness 的 Pipeline + 多个 Eino Executor（更可观测）

把每个 worker 实现成独立的 Eino Executor（或 CLI executor），用 §2.1 的 Pipeline 串起来。

好处：
- 每段独立 ExecutionProcess，前端能看每段 conversation
- 任意一段失败可以用户介入 follow-up
- 跨段可以混 CLI agent 和 Eino agent

代价：
- 段间共享状态只能走 git diff / handoff template，不如 in-process 灵活
- 不能做"supervisor 动态选 worker"——链表是固定的

### 5.3 推荐策略

- **决策类、需要循环 / 自适应**：用 5.1，丢给 Eino 内部跑
- **流水线、用户要看每步、混合 CLI 和进程内 agent**：用 5.2
- **真不知道选哪个时**：用 5.2，因为它能降级到单 agent

---

## 6. 三个具体场景走一遍

### 场景 A：Plan → Code → Review（Pipeline）

用户在前端点 "Run with workflow: plan-code-review"。前端构造：

```json
{
  "type": "CodingAgentInitialRequest",
  "body": {
    "prompt": "Read the codebase and produce an implementation plan in plan.md",
    "executor_config": { "executor": "CLAUDE_CODE" }
  },
  "next": {
    "type": "HandoffRequest",
    "body": { "pick_from": "git_diff", "template": "Implement the plan in plan.md.\n\nPrevious diff:\n{{.GitDiff}}" },
    "next": {
      "type": "CodingAgentInitialRequest",
      "body": { "prompt": "<placeholder>", "executor_config": { "executor": "CODEX" } },
      "next": {
        "type": "CodingAgentInitialRequest",
        "body": {
          "prompt": "Review the diff against plan.md. Comment in REVIEW.md.",
          "executor_config": { "executor": "GEMINI" }
        }
      }
    }
  }
}
```

Conductor 跑：
1. Spawn Claude，等 wait → 写出 plan.md
2. Handoff：跑 `git diff base...HEAD`，填进下一段 prompt
3. Spawn Codex（拿到注入后的 prompt）→ 实现
4. Spawn Gemini → review，写 REVIEW.md

前端三个 tab，分别是三段 conversation。Diff 视图是累计的 worktree diff。

### 场景 B：三个 agent 同题竞标（Parallel）

```json
{
  "type": "ParallelGroupRequest",
  "body": {
    "merge": "none",
    "branches": [
      { "type": "CodingAgentInitialRequest", "body": { "prompt": "Fix the login bug", "executor_config": { "executor": "CLAUDE_CODE" }}},
      { "type": "CodingAgentInitialRequest", "body": { "prompt": "Fix the login bug", "executor_config": { "executor": "CODEX" }}},
      { "type": "CodingAgentInitialRequest", "body": { "prompt": "Fix the login bug", "executor_config": { "executor": "GEMINI" }}}
    ]
  }
}
```

Conductor：
1. 在 group lock 下创建三个 worktree（branch: `fix-login-a3f-claude` 等）
2. 并发起三个 ExecutionProcess
3. 全跑完 → group status = completed
4. 前端展示三列 diff，用户挑一个 → "merge this branch into main"

### 场景 C：Eino supervisor 单跑

```json
{
  "type": "CodingAgentInitialRequest",
  "body": {
    "prompt": "Refactor the auth module",
    "executor_config": { "executor": "EINO_SUPERVISOR" }
  }
}
```

只有一段。harness 看到的：一个进程在跑。实际上 EinoSupervisor 内部的 Eino MultiAgent graph 在 supervisor 决策下交替调用 planner/coder/critic worker，stdout 流是合成的 NDJSON。

---

## 7. 数据模型增量汇总

```sql
-- workspace 增加并行支持
ALTER TABLE workspaces ADD COLUMN kind TEXT NOT NULL DEFAULT 'primary';
ALTER TABLE workspaces ADD COLUMN parent_workspace_id TEXT REFERENCES workspaces(id);
ALTER TABLE workspaces ADD COLUMN parallel_group_id TEXT;
CREATE INDEX idx_workspaces_group ON workspaces(parallel_group_id);

-- 新表：parallel_groups（可选，简单时直接用上面的字段就够）
CREATE TABLE parallel_groups (
    id          TEXT PRIMARY KEY,
    task_id     TEXT NOT NULL REFERENCES tasks(id),
    strategy    TEXT NOT NULL,            -- none/first_success/reviewer
    status      TEXT NOT NULL,            -- running/completed/failed/cancelled
    reviewer_workspace_id TEXT REFERENCES workspaces(id),
    created_at TIMESTAMP NOT NULL,
    finished_at TIMESTAMP
);

-- session 增加链路追踪
ALTER TABLE sessions ADD COLUMN stage_index INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sessions ADD COLUMN previous_session_id TEXT REFERENCES sessions(id);
```

`stage_index` 让前端能区分 "这是 plan-code-review 流的第几段"。

---

## 8. API 增量

```
POST /api/tasks/:id/workflows
     body: { type: "pipeline" | "parallel", ...action_chain }
     # 等价于 POST /workspaces，但 body 是完整 action 链

GET  /api/parallel-groups/:id
     # group 状态 + 所有 branch workspace + 各自 latest exec status

POST /api/parallel-groups/:id/promote
     body: { workspace_id }
     # 选定某个 branch，merge 到 base 或者切换为 primary
```

普通 pipeline 不需要新 API，复用现有 `POST /workspaces`，只是 action 链更长。

---

## 9. UI 改动

1. **Action 链可视化**：workspace 详情页顶部加 stepper，显示每段 stage（icon = executor logo，状态 = 颜色）。点哪段切到该段的 conversation
2. **Parallel group 视图**：task 详情页能显示"3 个并行 attempt"，每个一行，diff 数 + 进度
3. **Handoff 卡片**：在 stage 之间显示一个小卡片"context handed off: git_diff (847 lines)"，点开看具体内容
4. **Workflow 模板**：用户可以把"plan → code → review" 存成模板复用

UI 不在 Go 后端范围，但 API 要能撑起这些视图。

---

## 10. 落地顺序

跟主设计 §10 的里程碑对齐：

- **M3.5（在 M3 之后插一段）**：实现 Pipeline + HandoffRequest。这是 80% 多 agent 价值的地方
- **M5.5**：在 M5（Eino executor）之后做 ParallelGroup
- **M6+**：Iterative review、reviewer-merge、模板系统

不要在 M3 之前做任何多 agent 抽象。先把单 agent 闭环跑顺。多 agent 的接口只有在你已经手动用单 agent 串过几次"plan → code → review"觉得别扭时，设计才会准。

---

## 11. 风险清单

- **Handoff prompt 爆 token**：上一段 conversation 太长，模板渲染后超 context window。对策：截断 + 只保留 last assistant message + git diff，不传整段 raw conversation
- **Parallel 时 token 烧钱**：同一任务起 N 倍。前端必须显式提示成本，并发数默认上限 3
- **不同 agent 对同一文件有不同改法（Pipeline）**：后段 agent 把前段改的撤掉。这不是 bug 是预期——但要在 UI 里清楚显示每段的 diff 增量，让用户能定位
- **Eino executor 的 cancel 不可靠**：Eino 的 stream cancel 取决于 ChatModel 实现。Go 版的 `ctx.Done()` 要传到 ChatModel 客户端，否则 kill 命令杀不死 LLM 调用，只是 detach
- **Subagent (L3) 跟 multi-agent (L1) 在 UI 上易混淆**：Claude 自己派的 Task subagent 渲染在它自己的 conversation 里（缩进展示），跟 L1 的 stage 切换是两回事。前端要明确区分 "stage 切换" vs "subagent 嵌套"

---

## 12. 常见疑问

**Q: 为什么不直接用 Eino 的 MultiAgent / ADK，全靠它做协作？**
A: Eino 的多 agent 是进程内函数调用层面的协作。harness 要管的是 worktree、子进程、人工介入、可恢复——这些 Eino 不做。把 Eino 看作"一种 Executor 内部的实现选择"，harness 在外面管编排，分层最干净。

**Q: Pipeline 跟用户连续手动 follow-up 有啥区别？**
A: Pipeline 是预先定义好链、自动推进、可以中途换 executor；follow-up 是同一 executor 同一 session 续上。Pipeline 跨 executor 跨 session，每段独立 worktree state snapshot（before_head_commit/after_head_commit）。

**Q: 能不能让 agent 自己决定要不要 spawn 下一段？**
A: 可以，但那是 L3（agent 内部决策），不是 L1。给 agent 暴露一个 tool `request_next_stage(executor, prompt)`，它写出来 harness 拦截后追加到 action 链。MVP 不做。

**Q: ParallelGroup 失败的 branch 怎么调试？**
A: worktree 不删（设置 `worktree_deleted=0`），用户能 cd 进去看。前端提供 "Open in IDE" 按钮（VSCode CLI）。

---

文档到此。要先动哪部分代码：
1. 把主设计文档 §4.2 的 `Action` 类型补上 `HandoffRequest` 和 `ParallelGroupRequest` 占位（不实现）
2. 在 M3.5 写 Conductor + HandoffRequest
3. 在 M5.5 写 ParallelGroup

实现 Conductor 时，原项目 [`crates/services/src/services/container.rs`](../crates/services/src/services/container.rs) 里 `start_execution`/`spawn_action` 那段是参考样本——它已经处理了 next_action 推进，照着改就行。
