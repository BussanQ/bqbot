# bqbot：用 Go 重写 OpenMausBot 的实现方案

目标：用 Go 1.27.3 实现 [OpenMausBot](https://github.com/milind-soni/OpenMausBot) 的功能，界面用
[MyGo](https://github.com/egoist/mygo)（v0.7.1）的原生 UI（`package ui`，GPU 绘制，无 webview、无 Bun）。

## 总体架构

```
┌─────────────── bqbot（单个 Go 二进制）───────────────┐
│ ui/        MyGo 原生 UI：侧栏 bot 列表、聊天、卡片      │
│   │  （阶段 1 进程内调用；阶段 2 起走 HTTP + SSE）      │
│ internal/harness  轮次调度、事件归一化、权限中介        │
│ internal/driver   claude / codex / grok / API 驱动     │
│ internal/store    ~/.bqbot：bots.json、transcript NDJSON│
└──────────────────────────────────────────────────────┘
          │ 子进程（stream-json / JSONL / JSON-RPC / ACP）
     claude CLI · codex CLI · grok CLI
```

和原项目的对应关系：

| OpenMausBot (TS) | bqbot (Go) |
|---|---|
| Electron + React UI | MyGo 原生 UI（`ui`、`ui/markdown`） |
| `server/drivers/*` | `internal/driver`（`Driver` 接口，每个引擎一个文件） |
| `server/harness/*` 注册表 + 事件总线 | `internal/harness` |
| `~/.openmausbot` | `~/.bqbot`（可用 `BQBOT_HOME` 覆盖） |
| 127.0.0.1:8799 HTTP + SSE | 阶段 2：`net/http` + SSE，同进程内嵌，也可单独 `bqbot serve` |

## 阶段划分

### 阶段 1：MVP（本次实现）
- 原生窗口：左侧 bot 列表，右侧聊天，可拖动分栏。
- Bot 的创建和删除：名称、引擎、模型（可留空用默认）、人设（system prompt）。
- 驱动：
  - `claude`：`claude -p --output-format stream-json --verbose --include-partial-messages`，用 `--resume <session_id>` 续接会话，人设走 `--append-system-prompt`。
  - `codex`：`codex exec --json`，续接用 `codex exec resume <thread_id>`。
  - `pi`：`pi -p --mode json`，续接用 `--session-id <id>`，人设走 `--append-system-prompt`。
  - `echo`：离线驱动，给演示和测试用。
- 流式 Markdown 回复，工具调用显示为活动行，可以停止当前轮次。
- 持久化：`bots.json`（0600），每个 bot 一个 NDJSON transcript、一个独立工作目录。
- 默认拒绝（fail-closed）：claude 用 `--permission-mode dontAsk`，codex 用 `sandbox_mode="read-only"`，pi 没有审批机制，只开放 `read,grep,find,ls`，MCP 只开 bqbot 自己的两个工具（见下），否则 `--no-mcp`。审批卡片在阶段 2 做，在那之前不放开写权限。
- 启动时探测引擎（`--version`），没装的引擎在新建表单里不出现。

### Bot 之间转交（简化版已实现，提前于阶段 4）
- 内置 stdio MCP 服务：`bqbot mcp <grant 文件>`，提供 `list_bots` 和 `ask_bot(name, question)`。它通过 127.0.0.1 随机端口回调正在运行的 app，每轮一个 bearer token。token 写在 0600 的临时 grant 文件里，不进 argv 和引擎配置；轮次结束时 token 作废、文件删除。
- 只有回答用户的轮次能拿到 bqbot 工具；回答另一个 bot 的轮次不给，所以转交深度上限为 1。目标 bot 正忙时直接拒绝，并把原因返回给提问的 bot。提问方停止时，目标的轮次也会停止。
- 目标 bot 在单独的会话里作答，不写进它和用户的主对话：每个“目标 × 提问方”一个会话，续接游标存在 `Bot.Handoffs[提问方 ID]`，transcript 存在 `handoffs/<目标>/<提问方>.ndjson`。同一个提问方再问时续接这个会话，换一个提问方就是另一个会话。删除任何一方时，对应的会话一起删掉。
- 系统提示里列出其他 bot 的名字和人设摘要。提问方的聊天里显示“已转交给 X”；目标的聊天里只显示“正在回答 X 的提问（单独的会话）”和停止按钮，不显示问答内容。
- 各引擎只放开这一个服务：claude 用 `--mcp-config` + `--strict-mcp-config` + `--allowedTools mcp__bqbot__ask_bot,mcp__bqbot__list_bots`；codex 用 `-c mcp_servers.bqbot.*`，`enabled_tools` 只开这两个工具，`default_tools_approval_mode="approve"`，`tool_timeout_sec=900`；pi 在 bot 目录写 `.pi/mcp.json`（`exposure: "direct"`，关闭 codemode），加上 `--approve`，并在 `--tools` 里加入 `mcp__bqbot__*` 两个工具。

#### 和 OpenMausBot 的差别（后续阶段补齐）
OpenMausBot 的转交工具是 `coordinate_bots`，配合 `list_bots`、`assignment_status`（见原项目 `server/drivers/agents-catalog-goldens/full.tools-list.json`、`server/runtime-coordinator.ts`）。当前简化版的差别和补齐计划：

| 方面 | OpenMausBot | bqbot 当前 | 补齐阶段 |
|---|---|---|---|
| 工具 | `coordinate_bots`，一次 1 到 4 个 bot，按 ID 或唯一名字 | `ask_bot`，一次 1 个，按名字 | 阶段 4 |
| 等待方式 | 提问方发完就结束本轮，结果回来时自动唤醒它继续 | 提问方在工具调用里同步等待，最长 15 分钟 | 阶段 3（需要线程和事件总线） |
| 目标正忙 | 排在目标已有工作之后 | 直接拒绝 | 阶段 2（排队）+ 阶段 4 |
| 作答位置 | 每个提问方一个常驻工作线程，`thread: "new"` 可开新线程，在侧栏显示为目标下的一行 | 每个提问方一个单独会话，界面里看不到内容 | 阶段 3（线程列表、`list_threads` / `close_thread`） |
| 深度 | 上限 4 层，禁止回头问派活给自己的人或更上层 | 上限 1 层 | 阶段 4 |
| 模式 | `consult`（只回答，不动手）、`reply: "none"`（单向通知）、`rework`（要求返工）、`done_when` | 只有问答 | 阶段 4 |
| 状态查询 | `assignment_status` 只读查询进度和结果 | 没有 | 阶段 4 |
| 权限 | Chief of Staff 的审批级别沿转交链向下传递 | 目标用它自己的默认权限（只读） | 阶段 2 审批完成后 |
| 房间 | `list_room_targets`，在房间里默认发到本房间 | 没有 | 阶段 3（频道） |

### 阶段 2：Harness 服务与审批
- 抽出 HTTP + SSE 服务（127.0.0.1:8799，随机 bearer token），UI 改为通过它收发；每个线程的运行时事件写 NDJSON。
- 权限中介：claude 通过 `--permission-prompt-tool` 接一个 Go 写的 stdio MCP 服务，codex 改用 `codex app-server`（JSON-RPC 审批请求），pi 改用 `--mode rpc` 加一个扩展，在工具调用前通过 extension UI 请求确认。在聊天里显示 Allow / Deny / Answer 卡片，提供审批模式（Ask / 自动编辑 / 完全自动）。
- 模型目录和选择器（按引擎分组，对话中途可切换），提供 effort 档位。
- 引擎设置：自定义 CLI 路径；会话失效时用 transcript 重放恢复；运行中追加消息（steer / 排队）；图片附件。
- 转交排队：目标 bot 正忙时，把转交请求排在它当前的轮次之后，不再直接拒绝。
- grok CLI（ACP）驱动，以及 OpenAI 兼容 API 驱动（自带 transcript 重放）。

### 阶段 3：像聊天应用一样管理 bot
- 右键菜单：置顶、标为未读、改资料、复制、复制会话 ID、隐藏、删除；头像。
- 频道（群聊）：独立 transcript、共享指令、工作目录、成员名单、@提及、回复规则（lead / Auto）。
- 决策模型（OpenRouter）在 Auto 模式下挑选回复的 bot，不确定时回落到 lead。
- 记忆召回、上下文压缩、transcript 搜索。
- 线程：一个 bot 可以有多个对话线程，侧栏里显示在 bot 下面；转交会话迁移为可查看的线程，结果回来时唤醒提问方（见“和 OpenMausBot 的差别”）。

### 阶段 4：工具与自动化
- 自定义 MCP 服务器配置（stdio / http / sse）。
- Composio 连接应用：密钥存系统钥匙串（`mygo` secrets），UI 只拿到“已配置”标记。
- agents MCP 扩展：按上面的差别表，把 `ask_bot` 升级为 `coordinate_bots`，加入 `assignment_status`、深度 4、consult / 单向通知 / 返工；list_bots / ask_bot 简化版已实现。
- 触发器：一次性、按工作日、按间隔的日程（上一轮没跑完就跳过）；webhook 接收器（127.0.0.1:8800，只开放 `/health` 和 `/hooks/...`，bearer 鉴权）；运行历史。

### 阶段 5：Bot 的电脑
- 电脑面板：云桌面（Boat API）带实时截图预览，本地 VM，宿主机控制（Cua Driver，需要显式开启）。截图折叠进 transcript。

### 阶段 6：语音
- TTS：ElevenLabs、Fish Audio、xAI、macOS `say`、本地 Chatterbox，用 `mygo/audio` 播放；朗读回复，通话模式；听写。

### 阶段 7：团队、分享、无界面运行
- 导入团队 Markdown（YAML frontmatter）；导出部门时清除秘密，导入的 routine 默认暂停；预设 bot。
- 无界面 CLI：`bqbot team apply`、`bqbot run --json --export`，退出码可供脚本判断。
- stdio MCP 控制面（受限：不开放审批、删除、凭据）。
- 手机或远程访问：配对码，Tailscale 或反向代理。

### 阶段 8：发布
- `mygo build` 打包 dmg / exe 安装包 / deb，签名、公证、自动更新；分析统计默认关闭。

不实现的部分：`enterprise/` 目录（另有许可）、BotMRR 和 SupaMaus 等品牌内容。
