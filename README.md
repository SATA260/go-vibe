Go Vibe 是一个围绕 coding agent 的工作台：用 issue/kanban 做计划，用 workspace 隔离执行，用 git worktree 承载代码变更，用日志、diff、预览浏览器和 PR 流程帮助人工审查并推进交付。

它不是某一个 coding agent 本身，而是一个 harness：

- 对上提供 Web UI、桌面壳、MCP 工具和 Review CLI。
- 对中管理工作区、会话、执行进程、日志、审批、文件、配置和远端同步。
- 对下适配 Claude Code、Codex、Gemini、OpenCode、Amp、Cursor、Qwen、Copilot、Droid 等 coding agent。

## M0 启动

后端：

```bash
go run ./cmd/server
```

默认监听 `:8080`，SQLite 数据库写到 `./data/go-vibe.db`。可以用环境变量覆盖：

```bash
GO_VIBE_ADDR=:8081 GO_VIBE_DB=./data/dev.db go run ./cmd/server
```

健康检查：

```bash
curl http://localhost:8080/api/health
```

前端：

```bash
cd web
npm install
npm run dev
```

前端默认请求 `http://localhost:8080/api/health`，也可以用 `VITE_API_BASE` 覆盖。
