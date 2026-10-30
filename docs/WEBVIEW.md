# WebView 桌面界面

桌面入口使用 MyGo v0.7.1 的系统 WebView：macOS 上为 WKWebView。前端资源通过 Go `embed` 打包，由 `mygo.SetFrontend` 提供，不需要 Node 开发服务器、网络字体或 CDN。`go run .` 和 `go build -o build/bqbot .` 即可运行和构建；`bqbot serve` 与 `bqbot mcp` 入口不变。

## 文件与数据流

- `web/index.html`：页面骨架、语义标签和内容安全策略。
- `web/style.css`：浅色 / 深色主题、布局、窄窗口适配和减少动画偏好。
- `web/app.js`：会话导航、消息渲染、草稿、图片、审批和管理表单。
- `web/phase4.js`：MCP、Composio、自动任务、运行历史与 webhook 设置表单。
- `web/markdown.js`：轻量 Markdown 子集，支持标题、列表、引用、表格、代码块和 HTTP(S) 链接；原始 HTML 始终转义，不执行消息内容。
- `webview_app.go`：`Desktop.State`、显式 `Desktop.Action` 命令及文件选择、复制、外部链接操作。
- `phase4_webview.go`：`Desktop.Phase4` 显式命令，连接工具与自动化管理接口。

页面通过 `mygo.call` 调用 Go，Go 继续使用 `internal/service.Client`。服务 token 不进入网页，也不暴露通用 HTTP 代理。后台 SSE 使用现有 `app.fold` 状态归并逻辑，通过 `desktop:changed` 通知页面读取快照；快照在锁内序列化。浏览器侧合并刷新请求，按消息 ID 更新 DOM。

`view.go`、`phase2_view.go`、`phase3_view.go` 中的状态归并和会话操作仍被复用，原生绘制代码暂保留用于旧测试；生产窗口不再使用 `ui.View`。

## 交互

- `Enter` 发送，`Shift+Enter` 换行；中文输入法选词时不发送。
- `⌘/Ctrl+K` 搜索，`⌘/Ctrl+N` 新建当前伙伴的对话。
- 忙碌时可停止、排队或追加指令；切换会话保留本次运行中的草稿和附件。
- 主题默认跟随系统，手动选择保存在 WebView 本地存储中。
- 伙伴、会话、群聊、引擎设置、审批、记忆和搜索继续使用现有服务校验。
- 图片通过原生文件选择器导入，保留已有数量、类型和大小限制。

## 工具与自动化

从「引擎设置 → 工具与自动化」进入：

- **MCP 工具服务器**：添加、编辑、删除 stdio / HTTP / SSE 服务器，选择可用 Bot；环境变量和请求头填写变量名映射，实际值由 Go 从进程环境读取。
- **Composio 应用连接**：保存非秘密配置、设置或移除系统凭据存储中的 API 密钥、打开授权链接、查看应用连接状态。密钥保存后不回显，状态只显示是否已配置。
- **日程与 Webhook**：创建一次性、工作日、间隔或 webhook 任务，手动运行、查看历史、删除任务；另有接收器启停和密钥轮换入口。Webhook 密钥仅在生成时显示并可复制。

这些表单通过独立的 `Desktop.Phase4` 命令调用服务，不复用聊天快照传递密钥。调度、权限和持久化仍由 Go 服务负责。详细参数、接口和限制见 [阶段 4 说明](PHASE4.md)。

阶段 4 表单已接入代码，但实际 WebView 交互及浅深色视觉验收尚未完成；本次文档更新按要求不继续测试。

## 定向验证

以下是已有验证命令的使用说明，本次未执行。

```sh
go test . -run 'TestDesktop|TestWebFrontend' -count=1
node --test tests/markdown.test.mjs
go build -o build/bqbot .
```

Go 测试覆盖会话切换期间的发送归属、独立会话、只读关闭状态、群聊、记忆、搜索和嵌入资源。JavaScript 测试覆盖 Markdown 子集与 HTML 转义。视觉检查应使用实际 MyGo WebView，检查浅深色、窄窗口、长消息、表单和审批卡片；旧 `TestChatFrames` 只生成原生 UI 的截图。
