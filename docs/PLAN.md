# bqbot：用 Go 重写 OpenMausBot 的实现方案

> 界面实现更新：桌面入口现已使用 MyGo 的系统 WebView 和内嵌前端资源，详见 [WebView 桌面界面](WEBVIEW.md)。下文的原生 Go UI 描述保留为初期方案记录；服务与引擎架构继续沿用。

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

### 阶段 1：MVP（已完成；以下记录阶段 1 的协议与权限设计）
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

### Bot 之间转交与协调（阶段 4 核心实现已落地）

- 内置 stdio MCP 服务：`bqbot mcp <grant 文件>`，提供 `list_bots`、兼容入口 `ask_bot`、`coordinate_bots`、`assignment_status`、`list_threads`、`close_thread` 和 `list_room_targets`。服务通过 127.0.0.1 随机端口回调 app，每轮一个 bearer token；token 写入 `0600` 临时 grant 文件，不进入 argv，轮次结束后失效并删除文件。
- `coordinate_bots` 每次向 1–4 个不同目标派发任务，按 Bot ID 或唯一名称选择目标。目标正忙时与用户消息共用 FIFO。转交深度上限为 4，禁止转交给自己或祖先；每轮最多派发 8 个任务，同一协调链最多 32 个。
- 私人转交使用“目标 × 提问方”线程，`thread: "new"` 可创建新线程；频道转交留在当前频道，仅限频道成员。目标完成后默认在原会话自动唤醒提问方，多层结果逐层返回；续答可在链路限制内继续派发。
- `consult` 沿链继承，只允许审批层认可的读取工具，并禁用外部 MCP；`reply: "none"` 不唤醒发起者；`rework` 沿原线程返工；`done_when` 把验收条件交给目标说明，不自动执行验收。
- `assignment_status` 查询当前会话可见的任务进度和结果。停止、关闭或删除会取消关联工作；重启后未完成任务标记为 `interrupted`，不会自动重放。
- 审批模式沿转交链传递，审批 grant 与工具 grant 分离。外部 MCP 通过 bqbot 网关接入，并按 Bot 作用域和每轮审批控制调用。

当前行为、使用示例和验证边界见 [阶段 4 说明](PHASE4.md)。阶段 1–3 的单层转交限制属于历史设计。

### 阶段 2：Harness 服务与审批（已实现）

使用方式、接口和验证边界见 [阶段 2 说明](PHASE2.md)。外部引擎通过模拟协议与本机离线握手验证，提供商在线调用仍需对应账号环境验收。

- [x] 抽出 HTTP + SSE 服务（127.0.0.1:8799，随机 bearer token），UI 改为通过它收发；每个线程的运行时事件写 NDJSON。
- [x] 权限中介：claude 通过 `--permission-prompt-tool` 接一个 Go 写的 stdio MCP 服务，codex 改用 `codex app-server`（JSON-RPC 审批请求），pi 改用 `--mode rpc` 加一个扩展，在工具调用前通过 extension UI 请求确认。在聊天里显示 Allow / Deny / Answer 卡片，提供审批模式（Ask / 自动编辑 / 完全自动）。
- [x] 模型目录和选择器（按引擎分组，对话中途可切换），提供 effort 档位。
- [x] 引擎设置：自定义 CLI 路径；会话失效时用 transcript 重放恢复；运行中追加消息（steer / 排队）；图片附件。
- [x] 转交排队：目标 bot 正忙时，把转交请求排在它当前的轮次之后，不再直接拒绝。
- [x] grok CLI（ACP）驱动，以及 OpenAI 兼容 API 驱动（自带 transcript 重放）。

### 阶段 3：像聊天应用一样管理 bot（已实现）

使用方式、迁移与验证边界见 [阶段 3 说明](PHASE3.md)。Auto 路由通过本地模拟服务验证；OpenRouter 真实在线调用仍需账号环境验收。

- [x] 右键菜单：置顶、标为未读、改资料、复制、复制会话 ID、隐藏与恢复、删除；自定义文字或 emoji 头像。
- [x] 频道（群聊）：独立 transcript、共享指令、工作目录、成员名单、@提及、回复规则（lead / Auto）。
- [x] 可配置 OpenRouter 决策模型在 Auto 模式下选择回复者，缺配置、请求失败或不确定时回落到 lead。
- [x] 会话记忆管理与本地召回、保留原文的手动上下文压缩、transcript 全文搜索与跳转。
- [x] 多个独立线程及侧栏列表；旧转交会话原位迁移；异步转交完成后自动唤醒提问方，支持新线程与关闭、取消。

### 阶段 4：工具与自动化（已实现，本地定向回归通过）

使用方式、接口、持久化与限制见 [阶段 4 说明](PHASE4.md)。以下勾选表示已有代码实现，不代表全部场景已通过验收。

- [x] 自定义 MCP 服务器配置（stdio / HTTP / SSE）、Bot 作用域、每轮工具发现与审批网关。
- [x] Composio：通过 `mygo` secrets 保存 API 密钥、创建授权链接、查看连接状态，并接入 MCP；状态接口不回传密钥。
- [x] agents MCP：保留 `ask_bot`，新增 `coordinate_bots` 和 `assignment_status`，支持四层协调、consult、单向通知、返工及验收条件。
- [x] 自动化：一次性、工作日、间隔和 webhook 触发；上一轮及关联协调链未结束时跳过重叠触发，持久化运行历史和重启中断状态。
- [x] Webhook 接收器：127.0.0.1:8800，仅开放 `/health` 和 `/hooks/{id}`，使用独立 bearer 密钥，支持开启、关闭和轮换。
- [x] WebView 设置入口：MCP、Composio、自动任务、运行历史与 webhook 管理。
- [x] 协调、自动化、外部 MCP、Composio 模拟接口、桌面桥接的定向回归通过；真实 WebView 完成对话与各阶段设置入口操作，检查浅深色和窄窗口聊天截图。
- [ ] 真实 Composio 授权、系统钥匙串及外部引擎端到端行为仍需相应账号环境验收。

### 阶段 5：Bot 的电脑（已实现连接与控制）

详见 [阶段 5](PHASE5.md)。

- [x] Boat 云桌面、已有本地/远程 VM 的 Cua MCP、macOS 内置 Cua Driver 及权限引导；本机无需部署 MCP 服务。
- [x] 实时截图、鼠标键盘输入、截图折叠进 transcript；Bot 操作复用每轮审批，consult 禁用。
- [ ] 真实云桌面与 VM/宿主权限验收。VM 生命周期由现有虚拟机工具管理，应用不负责创建或启动 VM。

### 阶段 6：语音（已实现）

详见 [阶段 6](PHASE6.md)。

- [x] ElevenLabs、Fish Audio、xAI、macOS `say`、本地 Chatterbox 兼容接口，用 `mygo/audio` 播放；可停止及取消。
- [x] 单条回复朗读、自动朗读、通话模式、系统 WebView 听写能力检测。
- [x] 本机真实 WebView → macOS `say` → MyGo 播放完成链路验证。
- [ ] 云语音账号、Chatterbox 推理和真实麦克风听写仍需外部环境验收。

### 阶段 7：团队、分享、无界面运行（已实现）

详见 [阶段 7](PHASE7.md) 和 [远程访问](REMOTE.md)。

- [x] 团队 Markdown / YAML 导入、预览与部门导出；仅导出分享字段，导入 routine 默认暂停，Bot 使用 Ask；预设团队。
- [x] `bqbot team apply/export/presets`、`bqbot run --json --export`、独立线程、明确退出码与超时取消。
- [x] `bqbot control` 受限 stdio MCP，不开放审批、删除、凭据。
- [x] 远程浏览器聊天、短时单次配对码、设备 Bot 范围与撤销，供 Tailscale / HTTPS 反向代理转发。
- [ ] 实际手机与 Tailscale 环境验收；自由文本中手写的秘密仍须分享前审阅。

### 阶段 8：发布（打包与发布流程已实现）

详见 [发布说明](RELEASE.md)。

- [x] MyGo 打包配置、各平台 CI 产物流程、签名/公证配置注入、签名更新 UI；无统计 SDK。
- [x] 本机 macOS arm64 `.app` 与 DMG 开发打包通过。
- [ ] 正式证书签名、公证、Windows/Linux 安装和真实跨版本更新需要发布凭据与目标系统验收；没有发布 Release。

不实现的部分：`enterprise/` 目录（另有许可）、BotMRR 和 SupaMaus 等品牌内容。
