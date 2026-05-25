# Vibe Kanban 源码阅读总览

> 阅读范围：基于当前本地 checkout 的源码、README、docs、Cargo workspace、前后端入口文件和关键业务模块整理。  
> 注意：当前 `README.md` 明确标注 Vibe Kanban 正在 sunsetting；前端 `ProjectKanban` 当前直接渲染 `ProjectSunsetPage`，说明云端项目/看板功能在这个版本里已进入 export-only 状态。下面仍按源码中保留的完整架构说明各模块意义。

## 1. 项目一句话定位

Vibe Kanban 是一个围绕 coding agent 的工作台：用 issue/kanban 做计划，用 workspace 隔离执行，用 git worktree 承载代码变更，用日志、diff、预览浏览器和 PR 流程帮助人工审查并推进交付。

它不是某一个 coding agent 本身，而是一个 harness：

- 对上提供 Web UI、桌面壳、MCP 工具和 Review CLI。
- 对中管理工作区、会话、执行进程、日志、审批、文件、配置和远端同步。
- 对下适配 Claude Code、Codex、Gemini、OpenCode、Amp、Cursor、Qwen、Copilot、Droid 等 coding agent。

## 2. 主要功能

### 2.1 本地 workspace 执行 coding agent

- 创建 workspace 时为一个或多个 repo 创建独立 git worktree。
- 给 workspace 分配新分支，避免直接污染原始仓库。
- 启动 coding agent 初始请求或后续 follow-up 请求。
- 支持 setup script、cleanup script、archive script、dev server script。
- 保存和流式展示 agent 日志、命令日志、token/context 使用量、审批请求和错误。

关键入口：

- `crates/server/src/routes/workspaces/create.rs`
- `crates/server/src/routes/sessions/mod.rs`
- `crates/services/src/services/container.rs`
- `crates/local-deployment/src/container.rs`
- `crates/executors/src/executors/mod.rs`

### 2.2 工作区审查体验

- UI 里有 workspace sidebar、聊天区、日志区、diff/changes panel、右侧详情面板。
- 可以查看 git diff、文件树、运行日志、终端、workspace notes。
- 可以对 diff 留行内评论，再把反馈发回 agent。
- 可重试、reset 到指定历史进程、切换 session。

关键入口：

- `packages/web-core/src/pages/workspaces/WorkspacesLayout.tsx`
- `packages/web-core/src/pages/workspaces/ChangesPanelContainer.tsx`
- `packages/web-core/src/pages/workspaces/LogsContentContainer.tsx`
- `packages/web-core/src/pages/workspaces/WorkspacesMainContainer.tsx`

### 2.3 Git 和 PR 流程

- 注册本地 repo。
- 创建 workspace 分支和 worktree。
- 查看分支状态、ahead/behind、冲突状态、未提交文件。
- 支持 rebase、merge、push、force push。
- 支持 GitHub/Azure DevOps provider 检测和创建 PR。
- 可以附加已有 PR、读取 PR 评论、同步 PR 状态。

关键入口：

- `crates/git/src/lib.rs`
- `crates/git/src/cli.rs`
- `crates/git-host/src/lib.rs`
- `crates/server/src/routes/workspaces/git.rs`
- `crates/server/src/routes/workspaces/pr.rs`

### 2.4 预览浏览器和前端调试

- workspace 可以启动 dev server script。
- `preview-proxy` 用独立端口代理用户应用。
- 代理会注入 devtools、click-to-component、React fiber inspection、移动端调试脚本。
- 前端提供内置 preview browser、设备尺寸模拟、inspect mode。

关键入口：

- `crates/preview-proxy/src/lib.rs`
- `crates/server/src/routes/preview.rs`
- `packages/web-core/src/pages/workspaces/PreviewBrowserContainer.tsx`
- `packages/web-core/src/pages/workspaces/PreviewControlsContainer.tsx`

### 2.5 云端项目、issue 和团队协作

当前 UI 的项目功能已 export-only，但源码里仍保留完整 remote/cloud 服务：

- 云端 Axum API，路径为 `/v1/*`。
- PostgreSQL 存储组织、成员、项目、issue、评论、标签、PR、通知等。
- ElectricSQL 作为实时读路径，通过 `/shape/*` 提供 shape stream。
- 写入通过 REST mutation，返回 txid，让前端等待 Electric stream 追上再撤销乐观状态。
- 支持 GitHub/Google OAuth、本地自托管账号、组织邀请、附件、通知、GitHub App、billing feature gate。

关键入口：

- `crates/remote/src/app.rs`
- `crates/remote/src/routes/mod.rs`
- `crates/remote/src/shapes.rs`
- `crates/remote/src/mutation_definition.rs`
- `crates/remote/AGENTS.md`
- `packages/remote-web/src/app/entry/App.tsx`

### 2.6 Remote access / relay

- local backend 可以作为 relay host 注册到远端。
- remote web 可以通过 relay 访问本机的 `/api` 和 WebSocket。
- relay 请求使用签名、防重放时间戳、nonce、SPAKE2 配对、trusted key。
- 可用 WebRTC data channel 或 relay tunnel 转发。

关键入口：

- `crates/relay-client/src/lib.rs`
- `crates/relay-hosts/src/lib.rs`
- `crates/relay-control/src/lib.rs`
- `crates/relay-webrtc/src/*`
- `packages/remote-web/src/shared/lib/relayHostApi.ts`
- `packages/web-core/src/shared/lib/localApiTransport.ts`

### 2.7 MCP 工具

- `vibe-kanban-mcp` 暴露 workspace、session、repo、issue、organization 等工具。
- global mode 能操作较多资源。
- orchestrator mode 会限制在当前 workspace/session 上下文内，适合 agent 在某个 workspace 中自我管理任务。

关键入口：

- `crates/mcp/src/bin/vibe_kanban_mcp.rs`
- `crates/mcp/src/task_server/handler.rs`
- `crates/mcp/src/task_server/tools/mod.rs`

### 2.8 Review CLI

- `vibe-kanban review <pr-url>` 是独立 CLI。
- 它读取 GitHub PR、可附加 Claude Code session、clone PR 分支、打包上传到远端 review 服务。
- 适合大 PR 或 AI 生成代码的结构化审查。

关键入口：

- `crates/review/src/main.rs`
- `npx-cli/src/cli.ts`

## 3. 架构总览

```text
用户
  # 可以是浏览器、Tauri 桌面窗口、MCP client、npx 命令行、Review CLI
  |
  v
前端层
  packages/local-web
    # 本地 Web app 入口，使用 TanStack Router，调用本机 /api
  packages/remote-web
    # 远端 Cloud Web app，调用 /v1 和 /shape，也能经 relay 访问本机 /api
  packages/web-core
    # local-web 和 remote-web 共用的页面、hooks、dialog、状态、API client
  packages/ui
    # 更底层的共享 UI 组件库
  |
  v
本地后端层
  crates/server
    # Axum API server；挂载 /api 路由；生产模式也负责 serve 前端静态资源
  crates/local-deployment
    # Deployment 的本地实现；组装 DB、配置、Git、workspace、executor、relay 等服务
  crates/deployment
    # Deployment trait；把本地/云端部署差异抽象成统一接口
  |
  v
业务服务层
  crates/services
    # 容器/执行、配置、文件、事件、repo、搜索、通知、远端同步等业务服务
  crates/db
    # 本地 SQLite 模型和 SQLx migrations
  crates/workspace-manager
    # workspace 和 repo 的业务编排，负责把 repo 附加到 workspace
  crates/worktree-manager
    # 底层 git worktree 创建、校验、清理、重建
  |
  v
执行和代码层
  crates/executors
    # 各 coding agent 适配器；把统一 ExecutorAction 转成具体 CLI/JSON-RPC/ACP 调用
  crates/git
    # git2 + git CLI 操作封装
  crates/git-host
    # GitHub/Azure DevOps 的 PR provider 抽象
  |
  v
可选外围层
  crates/preview-proxy
    # 用户应用 dev server 的代理和调试脚本注入
  crates/mcp
    # MCP server，给外部 agent/client 调用 Vibe Kanban
  crates/review
    # 独立 PR review CLI
  crates/tauri-app
    # 桌面 app 壳，生产模式内嵌启动 server
  npx-cli
    # npx 包装器，下载平台二进制并启动 server / desktop / mcp / review
```

## 4. 本地核心执行链路

```text
1. 用户创建 workspace 并输入 prompt
   # 前端入口：CreateChatBox / create mode / workspace route
   # API：POST /api/workspaces/start
   |
   v
2. crates/server/src/routes/workspaces/create.rs
   # 校验 prompt 和 repos
   # 创建 Workspace 记录
   # 把 repo 绑定到 workspace
   # 导入附件，重写 attachment:// 链接为 .vibe-attachments/...
   |
   v
3. WorkspaceManager + WorktreeManager
   # WorkspaceManager：业务语义，知道 workspace 里有哪些 repo
   # WorktreeManager：git 语义，负责 create branch + git worktree add
   |
   v
4. LocalContainerService.start_workspace()
   # container 不是 Docker 容器，而是本地 workspace 目录 + 子进程生命周期管理
   # 会按项目配置决定是否先运行 setup script
   # 最终启动 coding agent initial request
   |
   v
5. ExecutorAction
   # ExecutorActionType 有四类：
   # - CodingAgentInitialRequest：第一次让 agent 干活
   # - CodingAgentFollowUpRequest：同一 session 继续对话
   # - ScriptRequest：setup/dev/cleanup/archive 等脚本
   # - ReviewRequest：审查类请求
   |
   v
6. executors crate
   # 根据 ExecutorConfig 选择 Claude/Codex/Gemini/OpenCode 等适配器
   # 负责 spawn 子进程、处理 approvals、标准化日志
   |
   v
7. MsgStore + DB + frontend stream
   # MsgStore：内存中的实时消息流
   # execution_processes：数据库里的进程记录
   # coding_agent_turns：agent 对话轮次和 session 信息
   # 前端通过 SSE/WebSocket/轮询展示日志、diff、状态
```

## 5. 重要领域名词

| 名称 | 源码位置 | 意义 |
| --- | --- | --- |
| `Project` | `crates/db/src/models/project.rs`, `api-types/src/project.rs` | 项目/看板容器。当前云端项目 UI 已 export-only，但远端模型仍存在。 |
| `Issue` | `api-types/src/issue.rs`, `crates/remote/src/db/issues.rs` | 云端看板里的任务项，有状态、优先级、评论、标签、父子关系。 |
| `Workspace` | `crates/db/src/models/workspace.rs` | 本地执行空间。一个 workspace 对应一个目标分支和一个隔离目录。 |
| `WorkspaceRepo` | `crates/db/src/models/workspace_repo.rs` | workspace 与 repo 的关联，保存 target branch。多 repo workspace 靠它表达。 |
| `Session` | `crates/db/src/models/session.rs` | workspace 里的 agent 对话线程。一个 workspace 可以有多个 session。 |
| `ExecutionProcess` | `crates/db/src/models/execution_process.rs` | 一次实际运行：agent、setup script、dev server、cleanup 等都会落成 process。 |
| `ExecutorAction` | `crates/executors/src/actions/mod.rs` | 对“要执行什么”的统一抽象，支持 action 链式串联。 |
| `ExecutorConfig` | `crates/executors/src/profile.rs` | 选择哪个 agent、variant、model、reasoning、permission policy。 |
| `ContainerService` | `crates/services/src/services/container.rs` | 执行编排接口。名字叫 container，但本地实现主要是 worktree 目录 + 进程管理。 |
| `LocalContainerService` | `crates/local-deployment/src/container.rs` | ContainerService 的本地实现，负责子进程、日志、状态和清理。 |
| `Deployment` | `crates/deployment/src/lib.rs` | 服务集合抽象。server 路由只依赖这个 trait，不直接绑定具体部署。 |
| `LocalDeployment` | `crates/local-deployment/src/lib.rs` | 本地部署实现，组装 SQLite、Git、配置、relay、workspace manager 等。 |
| `MsgStore` | `utils::msg_store` 使用点在 `services/container.rs` | 内存消息仓库，给执行日志和 SSE/WebSocket 做实时流。 |
| `Scratch` | `crates/db/src/models/scratch.rs` | 草稿和 UI 偏好存储，比如 follow-up draft、workspace notes、panel state。 |
| `Shape` | `crates/remote/src/shapes.rs` | ElectricSQL 实时订阅定义，客户端不能任意指定表，必须走服务端白名单。 |
| `Mutation` | `crates/remote/src/mutation_definition.rs` | 远端 CRUD 路由生成器，同时参与 TypeScript 类型生成。 |
| `RelayHost` | `crates/relay-hosts/src/lib.rs` | 被远端访问的本地机器，靠配对、签名和 session 保护。 |
| `PreviewProxy` | `crates/preview-proxy/src/lib.rs` | 代理 workspace dev server，并注入前端调试脚本。 |

## 6. 文件结构注释

```text
vibe-kanban/
├── Cargo.toml
│   # Rust workspace 根配置；包含大多数 crates
│   # 注意 crates/remote 和 crates/relay-tunnel 被 exclude，分别用独立命令构建/检查
├── package.json
│   # pnpm monorepo 根脚本；dev/check/format/lint/generate-types 都从这里调度
├── pnpm-workspace.yaml
│   # 只把 packages/* 纳入 pnpm workspace
├── README.md
│   # 用户视角介绍、安装、环境变量；当前标注 sunsetting
├── PROJECT_SOURCE_OVERVIEW.md
│   # 本文件，源码阅读总结
│
├── crates/
│   # Rust 后端和工具 crate 集合
│   ├── server/
│   │   # 本地 Axum server；/api 路由、frontend fallback、preview proxy 启动
│   │   ├── src/main.rs
│   │   │   # 独立 server 二进制入口；初始化部署、绑定主端口和预览代理端口
│   │   ├── src/startup.rs
│   │   │   # 给 Tauri 复用的 server 启动逻辑
│   │   ├── src/routes/
│   │   │   # 本地 API 路由；workspaces/sessions/repo/config/terminal/events 等
│   │   └── src/bin/generate_types.rs
│   │       # 生成 shared/types.ts；不要手改 generated file
│   │
│   ├── deployment/
│   │   # Deployment trait；server 对外只看到服务接口，不关心具体实现
│   ├── local-deployment/
│   │   # 本地部署实现；组装 DB、config、git、container、relay、preview 等
│   ├── services/
│   │   # 业务服务层；container/config/file/events/repo/search/remote_sync 等
│   ├── db/
│   │   # 本地 SQLite 数据层；models + migrations
│   ├── executors/
│   │   # coding agent 适配层；Claude/Codex/Gemini/OpenCode/Cursor/Amp 等
│   ├── workspace-manager/
│   │   # workspace 与 repo 的业务编排；创建和清理多 repo worktree 容器
│   ├── worktree-manager/
│   │   # git worktree 底层操作；负责 create/recreate/cleanup
│   ├── git/
│   │   # git2 + git CLI 封装；branch、diff、merge、rebase、push、worktree 等
│   ├── git-host/
│   │   # GitHub/Azure DevOps provider；创建 PR、读取 PR 评论、检测 provider
│   ├── api-types/
│   │   # local 和 remote 共享 API 类型；会导出到 TypeScript
│   ├── remote/
│   │   # 云端 hosted server；Postgres + ElectricSQL + OAuth + org/project/issue API
│   ├── remote-info/
│   │   # 本地进程保存 remote API base 等远端配置信息
│   ├── relay-client/
│   │   # 本地访问远端 relay API 的客户端；创建 session、配对、签名刷新
│   ├── relay-hosts/
│   │   # 本地作为 relay host 时的代理和凭据管理
│   ├── relay-control/
│   │   # relay 生命周期控制和请求签名服务
│   ├── relay-types/
│   │   # relay 请求/响应共享类型
│   ├── relay-protocol/
│   │   # relay WebSocket 协议枚举
│   ├── relay-ws/
│   │   # signed WebSocket 封装
│   ├── relay-webrtc/
│   │   # WebRTC data channel 代理实现
│   ├── relay-tunnel-core/
│   │   # relay tunnel 的 client/server 公共逻辑
│   ├── relay-tunnel/
│   │   # 独立 relay-server 二进制，workspace 中 exclude
│   ├── ws-bridge/
│   │   # WebSocket 双向转发工具
│   ├── preview-proxy/
│   │   # 预览代理；HTML 注入 devtools/click-to-component 脚本
│   ├── mcp/
│   │   # vibe-kanban-mcp；给 MCP client/agent 调用
│   ├── review/
│   │   # review CLI；上传 PR 代码包给远端 review 服务
│   ├── tauri-app/
│   │   # 桌面壳；生产模式启动本地 server 并打开 WebView
│   ├── desktop-bridge/
│   │   # 桌面/远程编辑器桥接相关类型和服务
│   ├── embedded-ssh/
│   │   # 内嵌 SSH server 支持
│   ├── trusted-key-auth/
│   │   # trusted key、SPAKE2、key confirmation 等安全配对基础设施
│   ├── client-info/
│   │   # 本地 backend 地址、preview proxy port 等运行时 client 信息
│   ├── server-info/
│   │   # server 版本/信息类小 crate
│   └── utils/
│       # 公共工具；assets 路径、response、diff、port file、browser、sentry 等
│
├── packages/
│   # TypeScript/React 前端 workspace
│   ├── local-web/
│   │   # 本地 app 入口；TanStack Router route files；Tauri listener；local auth provider
│   ├── remote-web/
│   │   # 远端 Cloud app 入口；OAuth、relay host API、remote shell
│   ├── web-core/
│   │   # 共用业务 UI；workspaces、kanban、hooks、dialog、API client、stores
│   ├── ui/
│   │   # 更基础的 UI 组件库，被 web-core/local-web/remote-web 复用
│   └── public/
│       # logo、截图等构建时静态资源
│
├── shared/
│   # Rust 生成给 TypeScript 的共享类型和 agent schema
│   ├── types.ts
│   │   # local backend 类型，由 crates/server/src/bin/generate_types.rs 生成
│   ├── remote-types.ts
│   │   # remote backend 类型，由 crates/remote/src/bin/generate_types.rs 生成
│   └── schemas/
│       # executor config JSON schema，例如 codex.json、claude_code.json
│
├── npx-cli/
│   # npm 包包装器；下载平台二进制，支持 main/mcp/review/desktop
├── docs/
│   # Mintlify 文档；用户指南、自托管、workspace、agent、cloud 文档
├── assets/
│   # 打包资源，包含 sounds/scripts 等
├── dev_assets_seed/
│   # dev 模式初始资产，例如空 DB seed
└── scripts/
    # 端口分配、DB prepare、i18n 检查、relay test client 等开发脚本
```

## 7. 前端结构

```text
packages/local-web/src/
├── app/entry/Bootstrap.tsx
│   # React 根入口；初始化 Sentry/PostHog/QueryClient/auth runtime/zoom
├── app/entry/App.tsx
│   # local runtime provider + router provider
├── app/router/index.ts
│   # TanStack Router createRouter(routeTree)
└── routes/
    # 文件路由；_app 是主布局，workspaces/project/onboarding/export 都从这里挂载

packages/web-core/src/
├── pages/workspaces/
│   # workspace 主体验：聊天、日志、diff、preview、右侧面板、sidebar
├── pages/kanban/
│   # kanban/project 相关组件；当前 ProjectKanban 渲染 sunset page
├── shared/lib/api.ts
│   # 本地 API client；导入 shared/types.ts 类型
├── shared/lib/remoteApi.ts
│   # 远端 /v1 API client；处理 bearer token 和 401 refresh
├── shared/lib/localApiTransport.ts
│   # local API transport；remote 页面可替换为 relay transport
├── shared/hooks/
│   # 数据请求、workspace 状态、git 操作、preview、terminal、notifications 等 hooks
├── shared/providers/
│   # WorkspaceProvider、ActionsProvider、TerminalProvider 等上下文
├── shared/stores/
│   # Zustand 状态，如 UI 偏好、diff view、workspace diff、组织选择
└── shared/dialogs/
    # command bar、settings、PR、rebase、OAuth、agent setup 等弹窗
```

前端的命名大致遵循：

- `Container`：负责取数据、组合 hooks、处理命令。
- `Provider`：React context 或跨组件状态提供器。
- `Dialog`：NiceModal 弹窗。
- `useXxx`：React hook，通常封装查询、mutation 或 UI 状态。
- `shared/lib`：无 UI 或低 UI 耦合的工具和 API client。

## 8. 后端本地 API 路由结构

`crates/server/src/routes/mod.rs` 是本地 API 总装点：

```text
/api/health
  # 健康检查
/api/config
  # 用户配置、agent 配置、编辑器检测、MCP servers
/api/workspaces
  # workspace CRUD、创建并启动、git、PR、execution、attachments、streams
/api/sessions
  # session CRUD、follow-up、reset、review、queue
/api/execution-processes
  # process 查询和日志
/api/events
  # SSE，合并历史事件和实时事件
/api/repo
  # repo 注册、初始化、搜索、branch/PR 相关
/api/filesystem
  # 文件系统浏览
/api/search
  # 文件搜索
/api/preview
  # preview proxy 相关 API
/api/terminal
  # terminal websocket
/api/ssh-session
  # SSH session websocket
/api/remote/*
  # 本地代理/同步远端 issue/project/PR 等资源
/api/host/*
  # remote web 经 relay 访问本地 backend 的代理入口
```

安全和中间件要点：

- 主 API 经过 origin 校验。
- relay-signed routes 会校验 relay 请求签名，并给响应签名。
- workspace/session 路由使用 middleware 预加载模型，避免 handler 重复查库。

## 9. 远端 Cloud 架构

```text
remote-web 浏览器
  # React app，使用 OAuth token
  |
  | REST mutation
  v
crates/remote /v1/*
  # Axum API；鉴权、组织成员权限、CRUD mutation
  |
  v
PostgreSQL
  # remote 的事实数据库
  |
  | logical replication
  v
ElectricSQL
  # 只读实时同步引擎
  |
  | /shape/* stream
  v
remote-web optimistic UI
  # mutation 返回 txid；前端等 Electric 追上 txid 后移除乐观状态
```

remote 里几个名字的意义：

- `ShapeDefinition`：声明 ElectricSQL 允许订阅哪个表、哪个 where 条件、哪个 URL。
- `MutationDefinition`：声明 REST CRUD mutation，同时输出 TypeScript metadata。
- `AppState`：远端路由共享状态，包含 PgPool、JWT、OAuth、mailer、R2/Azure、GitHub App、billing、analytics。
- `RequestContext`：鉴权中间件给受保护路由注入的用户上下文。

## 10. 数据存储和类型生成

### 本地

- SQLite 文件位置由 `utils::assets::asset_dir()` 决定，当前 DB 名是 `db.v2.sqlite`。
- `crates/db/src/lib.rs` 启动时自动运行 SQLx migrations。
- 主要本地表模型在 `crates/db/src/models/`：
  - `repo.rs`
  - `workspace.rs`
  - `workspace_repo.rs`
  - `session.rs`
  - `execution_process.rs`
  - `coding_agent_turn.rs`
  - `scratch.rs`
  - `file.rs`
  - `pull_request.rs`

### 远端

- PostgreSQL，由 `crates/remote/src/db/` 管理查询和 migrations。
- ElectricSQL 订阅定义在 `crates/remote/src/shapes.rs`。
- 远端行类型和请求类型主要放在 `crates/api-types/`，供 local/remote 共享。

### TypeScript 类型生成

```text
crates/server/src/bin/generate_types.rs
  # 生成 shared/types.ts
  # local-web/web-core 调本地 API 时使用

crates/remote/src/bin/generate_types.rs
  # 生成 shared/remote-types.ts
  # remote-web 和 web-core 调远端 API / Electric shape 时使用
```

不要手动改：

- `shared/types.ts`
- `shared/remote-types.ts`

## 11. coding agent 适配层

`crates/executors` 把不同 agent 统一成一个执行接口：

```text
StandardCodingAgentExecutor
  # 每个 coding agent 都实现这个 trait
  ├── spawn()
  │   # 初始 prompt
  ├── spawn_follow_up()
  │   # 继续已有 session
  ├── spawn_review()
  │   # review 请求，默认可复用 follow-up/initial
  ├── normalize_logs()
  │   # 把各 agent 私有日志转成统一 NormalizedEntry
  ├── default_mcp_config_path()
  │   # agent 的 MCP config 默认位置
  └── discover_options()
      # 动态发现模型、agent mode、reasoning 等可选项
```

当前已建模的 agent 名称：

- `ClaudeCode`
- `Codex`
- `Gemini`
- `Opencode`
- `Amp`
- `CursorAgent`
- `QwenCode`
- `Copilot`
- `Droid`
- `QaMock`，只在 `qa-mode` feature 下启用

`ExecutorConfig` 是前后端都传递的统一配置对象：

- `executor`：基础 agent 类型。
- `variant`：配置变体，比如 DEFAULT、PLAN。
- `model_id`：模型覆盖。
- `agent_id`：agent mode 覆盖。
- `reasoning_id`：reasoning effort 覆盖。
- `permission_policy`：权限策略覆盖。

## 12. 发布和运行形态

```text
npx vibe-kanban
  # 默认运行本地 browser mode
  # npx-cli 下载对应平台的 vibe-kanban 二进制并执行

npx vibe-kanban --desktop
  # 下载/启动 Tauri desktop app，失败时回退 browser mode

npx vibe-kanban mcp
  # 启动 vibe-kanban-mcp

npx vibe-kanban review <pr-url>
  # 启动 review CLI

pnpm run dev
  # 开发模式：同时启动 backend watch 和 local-web Vite dev server

pnpm run remote:dev
  # Docker compose 启动 remote-db、remote-server、electric
```

## 13. 当前代码里值得注意的状态

- README 和 `ProjectSunsetPage` 都表明项目/云端看板功能已经退役或 export-only。
- workspace、本地执行、agent 适配、preview、git/PR、MCP、relay 等代码仍然完整存在。
- `crates/remote` 不在根 Cargo workspace members 里，检查和生成类型要用 remote 专属脚本。
- `shared/types.ts` 的 header 写的是 `crates/core/src/bin/generate_types.rs`，但当前源码实际生成入口在 `crates/server/src/bin/generate_types.rs`。
- `ContainerService` 里的 container 命名容易误解，它主要不是 Docker，而是“workspace 目录 + worktree + 子进程”的抽象。
- `Project`/`Issue`/`Workspace` 在 local 和 remote 里有不同语境：Issue 更偏云端 kanban，Workspace 更偏本地执行环境。

## 14. 后续继续阅读建议

如果要继续深入，建议按这个顺序读：

1. `crates/server/src/routes/workspaces/create.rs`：从用户创建 workspace 进入。
2. `crates/services/src/services/container.rs`：理解 start_workspace、start_execution、action chain。
3. `crates/local-deployment/src/container.rs`：看本地子进程、日志、清理、状态更新。
4. `crates/executors/src/executors/mod.rs` 和具体 agent 文件：理解 agent 适配。
5. `packages/web-core/src/pages/workspaces/WorkspacesLayout.tsx`：理解 UI 如何组合 workspace 体验。
6. `crates/remote/AGENTS.md`、`crates/remote/src/shapes.rs`：理解云端实时同步。
7. `crates/relay-hosts/src/lib.rs` 和 `packages/remote-web/src/shared/lib/relayHostApi.ts`：理解 remote web 如何访问本机 workspace。
