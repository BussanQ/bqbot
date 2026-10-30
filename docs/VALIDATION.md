# 2026-10-30 实现与验收记录

本轮完成阶段 5–8 的代码、桌面接线与发布流程，并补做阶段 4 定向回归。没有创建分支、提交代码或发布 Release。下列结果不等于所有外部环境验收完成。

## 自动化验证

- 协调、自动化、外部 MCP、Webhook、模拟 Composio 和桌面桥接相关定向测试通过。新增组合测试确认：发起者轮次结束但委派仍运行时自动任务保持 running、重叠触发跳过，完整协调链返回后才完成（含 race）。
- 电脑适配、统一审批、consult 禁用、撤销和截图持久化测试通过（含 race）。
- 五种语音适配与配置、播放取消/替换、模拟 Chatterbox 测试通过（含 race）。
- 团队解析、脱敏字段、冲突拒绝、存储失败回滚、CLI独立线程/退出码/取消、受限 MCP 测试通过（含 race）。
- 远程配对范围、单次码、过期、尝试限制、origin、撤销测试通过（含 race）。
- Markdown 与语音前端行为共 5 项测试通过，新增 JavaScript 语法检查通过。
- macOS 本机 `go build`，Windows amd64、Linux amd64 交叉编译通过。交叉编译不代表已在目标系统启动。

遵循仓库要求，没有运行整个测试套件；执行的是受影响模块或指定测试。

## 实际运行

通过 `bqbot_smoke` 编译标签启动真实 MyGo WebView，隔离 `BQBOT_HOME` 并使用随机服务端口，完成：

1. 创建 Echo 伙伴、发送消息、等待回复显示。
2. 点击工具与自动化、语音、团队、远程访问、电脑入口，检查对话框标题。
3. 配置 macOS `say`，点击回复朗读，等待 MyGo 播放完成，无播放器错误。此检查证明播放链路执行完成，不对实际听感作判断。
4. 捕获并查看浅色、深色、720 px 窄窗口聊天截图，产物在忽略的 `build/smoke/`。

可复验：

```sh
go build -tags bqbot_smoke -o build/bqbot-smoke .
BQBOT_HOME="$(mktemp -d)" build/bqbot-smoke
```

`smoke_desktop.go` 仅在此标签下编译；会操作窗口、生成演示会话并自动退出，不用于发布版本。首次尝试遇到本机 8799 占用，随后隔离到随机端口验证通过。窄窗口截图中的 WKWebView 原生顶层 dialog 没有被 CapturePage 捕获，不能作为表单视觉验收依据；脚本检查确认设置 dialog 已打开。

实际 CLI 二进制在隔离服务中执行 `team apply`、`team export`、`run --json --export`、`control`，验证 JSON 结果、私有导出文件及受限工具列表，全部通过。

## 打包

```sh
go run github.com/egoist/mygo/cmd/mygo@v0.7.1 build -platform darwin/arm64
```

生成 `build/darwin-arm64/bqbot.app` 和 `bqbot-0.1.0-arm64.dmg`。当前为开发 ad-hoc 签名，没有 Developer ID 公证、没有签名更新产物，也没有外部发布。生产步骤见 [发布说明](RELEASE.md)。

## 内置本机控制补充验收

- Cua Driver 固定为 0.28.2；实际官方归档与二进制 SHA-256 已核验，6 项打包脚本测试通过。
- 内置驱动生命周期、取消、并发启动、旧审批失效、环境隔离、身份检查及无界面服务拒绝测试通过（race）；4 项电脑前端测试通过。
- 真实 WebView 打开本机驱动面板，显示驱动可用，辅助功能和屏幕录制均未授权；启动被阻止，未触发权限弹窗。聊天截图位于 `build/host-smoke/`；CapturePage 对原生 dialog 的限制仍适用。
- 实际驱动只读探测通过：MCP initialize/tools/list/check_permissions/health_report、0700 目录/0600 socket、关闭父进程保活管道后退出。探测脚本的父进程为 Python，health_report 正确报告其身份与 bqbot 不匹配；这不等于 GUI 宿主身份的授权后实测。
- `go run github.com/egoist/mygo/cmd/mygo@v0.7.1 build -skip-dmg` 生成包含驱动的 `.app`；主应用与内置二进制 `codesign --verify --strict` 通过。Windows/Linux amd64 交叉编译通过。
- 尚未实测已授权 GUI 中的截图/输入、生产签名公证、macOS 13 旧系统。使用步骤见 [电脑说明](PHASE5.md)。

## 仍需外部环境验收

- Composio 授权与系统钥匙串，真实 CLI/云模型、外部 MCP 提供商。
- Boat 账号、已有 VM/Cua、宿主屏幕和输入权限。VM 创建/启动由现有虚拟机工具管理。
- 云语音、Chatterbox 推理、真实麦克风和 WebView SpeechRecognition 支持。
- 手机、Tailscale/HTTPS 反向代理部署。
- 原生团队文件选择/保存，工具表单所有分支和长内容/审批的视觉交互。
- 正式签名证书、公证、Windows/Linux 运行安装、已发布版本之间的更新。
