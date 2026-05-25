Go Vibe 是一个围绕 coding agent 的工作台：用 issue/kanban 做计划，用 workspace 隔离执行，用 git worktree 承载代码变更，用日志、diff、预览浏览器和 PR 流程帮助人工审查并推进交付。

它不是某一个 coding agent 本身，而是一个 harness：

- 对上提供 Web UI、桌面壳、MCP 工具和 Review CLI。
- 对中管理工作区、会话、执行进程、日志、审批、文件、配置和远端同步。
- 对下适配 Claude Code、Codex、Gemini、OpenCode、Amp、Cursor、Qwen、Copilot、Droid 等 coding agent。