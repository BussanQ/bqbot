# 阶段 2：Harness 服务与审批

## 启动

```sh
go run .          # 原生窗口，内嵌本地服务
go run . serve    # 只运行服务，Ctrl+C 停止
```

服务监听 `127.0.0.1:8799`。每次启动生成随机 bearer token，连接信息写入数据目录的 `service.json`（0600）；不会把 token 放入命令行或日志。已运行 `serve` 时，桌面窗口读取同一数据目录的连接信息并连接服务。数据目录默认 `~/.bqbot`，开发和测试可通过 `BQBOT_HOME` 隔离。

窗口的 bot 管理、消息、停止、审批、引擎设置和历史读取均经过 HTTP；实时更新使用 SSE。断线后客户端重新连接并同步状态。服务只接受 loopback 请求，不提供跨域访问。

## 聊天操作

- **Model & permissions**：按引擎选择模型、设置 reasoning effort，以及修改审批模式。修改在下一轮生效，切换模型后用聊天记录建立新会话。
- **Ask**：工具请求显示 Allow / Deny 卡片；引擎向用户提问时显示 Answer 卡片。
- **Auto edit**：自动批准明确的文件编辑工具；命令执行和其他工具继续询问。
- **Fully automatic**：自动批准工具请求；需要用户回答的问题仍显示卡片。
- 回复过程中仍可发送消息：`queue` 等待当前轮次结束，`steer` 在支持的引擎中追加指令；不支持或附带图片时回退为排队。
- **Stop / ⌘.**：停止当前轮次、取消待审批请求和排队任务。
- **Attach image**：支持 PNG、JPEG、GIF、WebP；每张不超过 5 MiB，每条最多 4 张，总计不超过 12 MiB。图片随消息保存在 transcript 中。
- 目标 bot 忙碌时，转交与用户消息共用 FIFO 队列；停止提问方会取消其排队或执行中的转交。转交仍只允许一层，审批模式继承提问方。

## 引擎

| 引擎 | 轮次协议 | 审批入口 |
|---|---|---|
| Claude | print / stream-json | 独立 stdio MCP permission-prompt-tool |
| Codex | app-server JSON-RPC | 工具审批与用户问题请求 |
| Pi | RPC + 内置扩展 | tool_call 前的 extension UI 确认 |
| Grok | ACP | session/request_permission |
| OpenAI compatible | 流式 chat/completions | 无本地工具执行；逐轮重放 transcript |
| Echo | 离线 | 无工具 |

侧栏底部的 **Engine settings** 可配置 CLI 路径、自定义模型目录，以及 OpenAI 兼容服务的 base URL 和密钥环境变量名称。密钥从服务进程的环境读取，不在设置文件中保存明文，也不返回给 UI。模型目录默认使用预设；填写自定义目录会替换该引擎的预设列表，也可在聊天设置中直接输入模型 ID。提供商可用性仍由实际引擎决定。

原生会话恢复仅在引擎明确报告会话不存在且尚未生成内容或申请执行工具时重试一次，避免重放已经产生副作用的轮次。恢复使用此前的 transcript，保留图片；一般网络或模型错误不会触发自动重试。

## 数据与接口

- `bots.json`：bot、模型、effort、审批模式和原生会话游标。
- `settings.json`：引擎路径、API 地址、密钥环境变量名称和模型目录配置。
- `threads/<bot>.ndjson`：用户聊天记录。
- `handoffs/<target>/<asker>.ndjson`：独立转交聊天记录。
- `events/<bot>.ndjson`、`events/handoffs/<target>/<asker>.ndjson`：运行时事件。

HTTP 接口统一要求 `Authorization: Bearer <token>`，JSON 写请求要求 `Content-Type: application/json`。客户端实现见 `internal/service/client.go`。

| 方法 | 路径 | 用途 |
|---|---|---|
| GET / PUT | `/v1/bots` | 列出 / 创建或更新 bot |
| DELETE | `/v1/bots/{id}` | 停止任务并删除 bot |
| GET | `/v1/bots/{id}/transcript` | 用户聊天记录 |
| GET | `/v1/bots/{id}/handoffs/{asker}` | 转交聊天记录 |
| POST | `/v1/bots/{id}/send` | 发送或排队消息 |
| POST | `/v1/bots/{id}/stop` | 停止并清空队列 |
| POST | `/v1/bots/{id}/approvals/{request}` | 提交 allow / deny / answer |
| GET | `/v1/events` | SSE 事件与状态同步 |
| GET | `/v1/snapshot` | 当前运行状态 |
| GET | `/v1/engines` | 引擎可用性和模型目录 |
| GET / PUT | `/v1/settings` | 引擎设置 |

## 验证范围

使用临时数据目录、离线 Echo、模拟 CLI 协议进程和本地 HTTP 服务验证审批、流式输出、停止、排队、转交、会话恢复及图片传递。UI 使用 MyGo `ui.NewTester` 驱动真实控件，截图保存于忽略的 `build/shots`。

外部模型服务的实际登录、订阅可用性和模型输出需要对应提供商凭据；协议测试不代表这些外部服务已经完成在线验收。
