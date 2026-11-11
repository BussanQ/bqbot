# WebView 桌面界面

桌面入口使用 MyGo v0.7.1 的系统 WebView：macOS 上为 WKWebView。前端资源通过 Go `embed` 打包，由 `mygo.SetFrontend` 提供，不需要 Node 开发服务器、网络字体或 CDN。`go run .` 和 `go build -o build/bqbot .` 即可运行和构建；`bqbot serve` 与 `bqbot mcp` 入口不变。

## 文件与数据流

- `web/index.html`：页面骨架、语义标签和内容安全策略。
- `web/style.css`：浅色 / 深色主题、布局、窄窗口适配和减少动画偏好。
- `web/app.js`：会话导航、消息渲染、草稿、图片、审批和管理表单。
- `web/agents.js`：Agent 接入列表、模型筛选、思考强度和追加指令的可用条件。
- `web/tools.js`：MCP、Composio、自动任务、运行历史与 webhook 设置表单。
- `web/computer.js`、`web/voice.js`、`web/team.js`、`web/remote.js`：电脑预览与控制、语音与听写、团队分享和远程配对。
- `webgo/computer.go`、`webgo/voice.go`、`webgo/team.go`、`webgo/remote.go`：这些功能的封闭命令桥接；团队导出使用原生保存对话框。
- `web/markdown.js`：轻量 Markdown 子集，支持标题、列表、引用、表格、代码块和 HTTP(S) 链接；原始 HTML 始终转义，不执行消息内容。
- `webgo/desktop.go`：`Desktop.State`、显式 `Desktop.Action` 命令及文件选择、复制、外部链接操作。
- `webgo/tools.go`：`Desktop.Tools` 显式命令，连接工具与自动化管理接口。
- `webgo/window.go`：`webgo.Run` 窗口入口、前端绑定及桌面生命周期；根目录 `main.go` 负责进程与服务启动。
- `webgo/updates.go`：签名更新检查入口。
- `web/assets.go`：嵌入 `web/` 的 HTML、CSS 和 JavaScript，供 `webgo` 加载。

页面通过 `mygo.call` 调用 Go，Go 继续使用 `internal/service.Client`。服务 token 不进入网页，也不暴露通用 HTTP 代理。后台 SSE 使用现有 `app.fold` 状态归并逻辑，通过 `desktop:changed` 通知页面读取快照；快照在锁内序列化。浏览器侧合并刷新请求，按消息 ID 更新 DOM。

`webgo/chat_view.go`、`webgo/permissions_view.go`、`webgo/workspace_view.go` 中的状态归并和会话操作仍被复用，原生绘制代码暂保留用于旧测试；生产窗口不再使用 `ui.View`。

## 交互

默认伙伴 bqbot 提供内置模型和工具调用，输入区的电脑图标打开当前伙伴的电脑。Linux 工作区、实时视频和模型配置见 [工作区指南](WORKSPACE.md)。

- `Enter` 发送，`Shift+Enter` 换行；中文输入法选词时不发送。
- `⌘/Ctrl+K` 搜索，`⌘/Ctrl+N` 新建当前伙伴的对话。
- 在空工作区或「添加 AI 伙伴」选择 Agent。本机 CLI 显示版本检测结果；点「需配置」可填写路径并重新检测。API 入口可配置服务地址、密钥环境变量名和模型；Echo 可直接离线试聊。CLI 检测只确认命令可运行，账号登录由对应 CLI 管理。
- 输入区显示已选模型 ID，沿用默认时显示 Agent 报告的默认模型。点击可展开当前 Agent 的模型列表、推理强度及工具权限，选择后即保存。用「刷新」重新读取目录，读取失败时会提示并显示配置或内置列表。在「编辑 AI 伙伴」切换 Agent 或填写自定义模型 ID。设置适用于该伙伴的所有会话，从下一轮生效。
- 忙碌时默认排队，待处理消息显示在输入区上方。Codex 和 Pi 在可以追加纯文本时显示「追加指令」，也可用 `⌘/Ctrl+Enter` 请求追加。切换对话保留本次运行中的草稿和附件。Agent 提问时可点选建议答案或自由输入，再按「提交回答」。
- 展开回复下方的「工具与协作」可查看 CLI 和外部 MCP 工具的状态与文本输出。每次调用最多保留 8 KiB 预览，随消息保存。旧记录继续显示当时保存的活动名称。
- 主题默认跟随系统，手动选择保存在 WebView 本地存储中。
- 伙伴、会话、群聊、引擎设置、审批、记忆和搜索继续使用现有服务校验。
- 图片通过原生文件选择器导入，保留已有数量、类型和大小限制。

## 工具与自动化

从「引擎设置 → 工具与自动化」进入：

- **MCP 工具服务器**：添加、编辑、删除 stdio / HTTP / SSE 服务器，选择可用 Bot；环境变量和请求头填写变量名映射，实际值由 Go 从进程环境读取。
- **Composio 应用连接**：保存非秘密配置、设置或移除系统凭据存储中的 API 密钥、打开授权链接、查看应用连接状态。密钥保存后不回显，状态只显示是否已配置。
- **日程与 Webhook**：创建一次性、工作日、间隔或 webhook 任务，手动运行、查看历史、删除任务；另有接收器启停和密钥轮换入口。Webhook 密钥仅在生成时显示并可复制。

这些表单通过独立的 `Desktop.Tools` 命令调用服务，不复用聊天快照传递密钥。调度、权限和持久化仍由 Go 服务负责。详细参数、接口和限制见 [阶段 4 说明](PHASE4.md)。

设置同时提供伙伴的电脑、语音、团队分享、手机访问与检查更新入口；输入区提供听写、通话和电脑按钮，回复旁可单独朗读。电脑截图默认折叠。真实 WebView 已完成这些入口操作与 Echo 对话；详细验证范围见 [验收记录](VALIDATION.md)。

## 定向验证

按改动范围选择以下命令复验：

```sh
go test ./webgo -run 'TestDesktop|TestWebFrontend' -count=1
node --test tests/markdown.test.mjs
node --test tests/agents.test.mjs
go build -o build/bqbot .
```

Go 测试覆盖会话切换期间的发送归属、独立会话、只读关闭状态、群聊、记忆、搜索和嵌入资源。JavaScript 测试覆盖 Markdown 渲染与转义、模型思考强度和运行中追加指令的条件。视觉检查应使用实际 MyGo WebView，检查浅深色、窄窗口、长消息、表单和审批卡片；旧 `TestChatFrames` 只生成原生 UI 的截图。
