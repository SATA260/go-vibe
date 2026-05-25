# Vibe Kanban Go 版 · 文档索引与启动指南

> 这是 Go 版重写的入口。三份设计文档拆开是为了不打架，合起来才是完整方案。

## 文档地图

| 文档 | 一句话定位 | 读完知道 |
|------|-----------|---------|
| [vibe-kanban-go-design.md](vibe-kanban-go-design.md) | **主设计**：harness 骨架、核心抽象、API、数据模型、技术选型 | 整个项目长什么样、第一周写什么 |
| [vibe-kanban-go-multi-agent.md](vibe-kanban-go-multi-agent.md) | 多 agent 协作：Pipeline / Parallel / Iterative 三种模式 | 多个 agent 怎么共同跑一个任务、Eino 在哪一层进场 |
| [vibe-kanban-go-voice.md](vibe-kanban-go-voice.md) | 语音播报 + 陪跑：WebRTC + WS 信令 + ASR/TTS/VAD 抽象 | 单人语音协作怎么搭、什么时候不该说话 |

**推荐阅读顺序**：主设计 → 多 agent → 语音。前一篇是后一篇的前提；语音文档假设你已经懂主设计的事件流和 multi-agent 的 Action 链。

---

## 统一里程碑时间线

把三份文档的里程碑合到一条线上。每个 M 都是"能跑通一件具体事"的验证点，不是工时估算。

```
M0  骨架（chi + sqlite + /health）
M1  Echo executor 跑通 → MsgStore + SSE 端到端
M2  Claude Code executor + conversation 视图
M3  Action 链 + follow-up + diff 视图
├── VV0  文本播报：用浏览器 Audio() 验证规则     ← 越早做越省心
M3.5 Pipeline + HandoffRequest（多 agent 主菜）
├── VV1  Pion + WS 信令（echo bot）
├── VV2  接 VAD/ASR/TTS 各一个实现，端到端"你好"
M4  第二个 CLI agent + executor registry
├── VV3  VoiceOrchestrator + 只读工具
M5  Eino executor（in-process agent）
├── VV4  陪跑 + barge-in
M5.5 ParallelGroup（多 agent 并行竞标）
├── VV5  WS / ICE restart / cold restart 三档重连
├── VV6  第二个 ASR/TTS 实现验证抽象
└── VV7  成本闸门 + idle / PTT
M6+ Iterative review、reviewer-merge、远端部署……
```

V 系列（语音）跟 M 系列（主功能）能并行做，但 VV0 必须在 M3 完成后立刻插队——规则只有在真有 attempt 状态变化时才能验证。

---

## Day 1 启动清单

按顺序，半天到一天就能跑起来一个空壳服务：

1. `mkdir vibe-kanban-go && cd vibe-kanban-go && go mod init vibe-kanban-go`
2. 拉依赖：`chi` / `mattn/go-sqlite3` / `golang-migrate` / `evanphx/json-patch/v5` / `google/uuid` / `koanf` / `pion/webrtc/v4`（语音阶段再用）
3. 按主设计 §3 建目录骨架；空 `package` 也建上，避免后面来回挪
4. 写第一个 migration `0001_init.sql`：`repos / tasks / workspaces / sessions / execution_processes / execution_process_logs`（主设计 §5）
5. 起 `cmd/server/main.go`：chi Router + `/api/health` 返回 `{"ok":true}` + sqlite 连上 + migrate up
6. 复制原项目前端 [packages/local-web](../packages/local-web/) 的 vite 脚手架到 `web/`，把 API 基址指到本地 Go server，先跑空页面
7. 跑通 `pnpm dev` + `go run ./cmd/server` 同时起；前端能 fetch `/api/health` 看到 200

跑通这步后再开 M1。**不要先抽象 Executor 接口**——先在 main.go 里硬编码一个 echo 子进程跑通 SSE，所有抽象等 M2 接第二个 executor 时再提取。三件相同的事才抽象，两件不要抽。

---

## 三个最容易踩的早期决定

读完三份文档后，下面这几个点最值得你先想清楚再动手：

1. **JSON Patch 协议是 harness 的"窄腰"**（主设计 §6 / 附录 C）。一旦前端 reducer 实现完，所有 agent 的差异都封死在各自的 `NormalizeLogs` 里。**先把这个协议跑顺，比先抽象 Executor 接口更重要**。

2. **Eino 不是核心，是一种 Executor**（多 agent 文档 §5）。如果发现自己在改 Eino 源码改得很深，停下来问：这件事是不是该在 Executor wrapper 里做？大概率是。

3. **语音的难点不是技术，是规则**（语音文档 VV0）。"什么时候说话"调不好，做完就关掉。VV0 的两天文本播报演练比 VV1–VV5 加起来还重要。

---

## 卡壳时回去看哪份原代码

| 不确定怎么写 | 看 Rust 原文件 |
|-----------|--------------|
| 编排一个 attempt 的生命周期 | [crates/services/src/services/container.rs](../crates/services/src/services/container.rs) |
| Executor 接口边界 | [crates/executors/src/executors/mod.rs](../crates/executors/src/executors/mod.rs) |
| 解析 stream-json 出 NormalizedEntry | [crates/executors/src/executors/claude.rs](../crates/executors/src/executors/claude.rs) |
| MsgStore + 历史回放 | [crates/utils/src/msg_store.rs](../crates/utils/src/msg_store.rs) |
| DB 变更广播 | [crates/services/src/services/events.rs](../crates/services/src/services/events.rs) |
| Worktree 创建并发控制 | [crates/worktree-manager/src/worktree_manager.rs](../crates/worktree-manager/src/worktree_manager.rs) |
| Action 链 + Executable 派发 | [crates/executors/src/actions/mod.rs](../crates/executors/src/actions/mod.rs) |

读 Rust 不是为了照抄——是看"它处理了什么 corner case"。Go 版很多时候可以更简单（比如不需要 enum_dispatch 那套宏），但不能漏掉它已经踩过的坑。

---

写不下去时回来读这页。Vibe coding 不是一口气写完，是每天能验证一件具体的事就够。