# WebView 桌面界面

桌面入口使用 MyGo v0.7.1 的系统 WebView：macOS 上为 WKWebView。前端资源通过 Go `embed` 打包，由 `mygo.SetFrontend` 提供，不需要 Node 开发服务器、网络字体或 CDN。`go run .` 和 `go build -o build/bqbot .` 即可运行和构建；`bqbot serve` 与 `bqbot mcp` 入口不变。

## 文件与数据流

- `web/index.html`：页面骨架、语义标签和内容安全策略。
- `web/style.css`：浅色 / 深色主题、布局、窄窗口适配和减少动画偏好。
- `web/app.js`：会话导航、消息渲染、草稿、图片、审批和管理表单。
- `web/markdown.js`：轻量 Markdown 子集，支持标题、列表、引用、表格、代码块和 HTTP(S) 链接；原始 HTML 始终转义，不执行消息内容。
- `webview_app.go`：`Desktop.State`、显式 `Desktop.Action` 命令及文件选择、复制、外部链接操作。

页面通过 `mygo.call` 调用 Go，Go 继续使用 `internal/service.Client`。服务 token 不进入网页，也不暴露通用 HTTP 代理。后台 SSE 使用现有 `app.fold` 状态归并逻辑，通过 `desktop:changed` 通知页面读取快照；快照在锁内序列化。浏览器侧合并刷新请求，按消息 ID 更新 DOM。

`view.go`、`phase2_view.go`、`phase3_view.go` 中的状态归并和会话操作仍被复用，原生绘制代码暂保留用于旧测试；生产窗口不再使用 `ui.View`。

## 交互

- `Enter` 发送，`Shift+Enter` 换行；中文输入法选词时不发送。
- `⌘/Ctrl+K` 搜索，`⌘/Ctrl+N` 新建当前伙伴的对话。
- 忙碌时可停止、排队或追加指令；切换会话保留本次运行中的草稿和附件。
- 主题默认跟随系统，手动选择保存在 WebView 本地存储中。
- 伙伴、会话、群聊、引擎设置、审批、记忆和搜索继续使用现有服务校验。
- 图片通过原生文件选择器导入，保留已有数量、类型和大小限制。

## 定向验证

```sh
go test . -run 'TestDesktop|TestWebFrontend' -count=1
node --test tests/markdown.test.mjs
go build -o build/bqbot .
```

Go 测试覆盖会话切换期间的发送归属、独立会话、只读关闭状态、群聊、记忆、搜索和嵌入资源。JavaScript 测试覆盖 Markdown 子集与 HTML 转义。视觉检查应使用实际 MyGo WebView，检查浅深色、窄窗口、长消息、表单和审批卡片；旧 `TestChatFrames` 只生成原生 UI 的截图。
