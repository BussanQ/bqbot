# 阶段 5：伙伴的电脑

电脑入口位于桌面设置中的「伙伴的电脑」。每个 Bot 可以配置一台已有桌面：Boat 云 Sandbox、运行 Cua Driver 的 VM，或者这台宿主机。连接默认关闭；访问宿主机还必须单独勾选允许屏幕和输入设备访问。

## 配置与使用

- **Boat**：API 地址使用 `https://boat.dev/api/v1`，填写已有的 Sandbox ID 和 API token 的环境变量名。Sandbox 必须处于可用状态，运行 X11 桌面并安装 `scrot`、`xdotool`。bqbot 不自动创建、收费、恢复或销毁云机器。
- **VM**：填写已有 Cua Driver 的 Streamable HTTP MCP 地址；可以使用 HTTPS，或通过 SSH 端口转发使用字面量回环 HTTP 地址。VM 的创建、启动和系统安装由现有虚拟化软件负责。
- **这台电脑（内置）**：macOS 13+ 的安装包随附 Cua Driver 0.28.2，无需填写 MCP 地址、安装独立服务或配置凭据。勾选宿主访问后，在「本机驱动与系统权限」中请求辅助功能和屏幕录制权限，再启动驱动。权限仅通过显式点击申请；授权后可能需要重启驱动或应用。Windows/Linux 暂不提供内置本机控制，仍可连接云桌面和 VM。
- **旧本机 HTTP 配置**：保留原有回环 MCP 地址，只有明确勾选迁移到内置驱动后才清空连接字段。

凭据只保存环境变量名，值在 Go 中读取。配置写入 `computers.json`（0600）。远程地址不允许 URL 用户信息、查询串或 fragment；HTTP 仅允许字面量回环地址，宿主类型始终限制回环地址。

打开电脑后，面板每三秒获取一次截图。关闭面板或离开面板后停止刷新。勾选面板的输入授权后，可点击画面、输入文字、按 Enter/Escape 和上下滚动。「发送截图到当前对话」重新获取截图并作为用户图片消息发送，当前对话必须属于该 Bot 且是开放的私人线程。图片附件名以 `computer-` 开头，聊天界面将其折叠显示。

## Bot 工具与审批

为支持 MCP 的引擎启用电脑后，后续轮次出现 `external_computer` 工具，支持 `screenshot`、`click`、`type`、`key`、`scroll`。工具没有任意 shell、文件读写或服务器配置入口。

每次调用复用外部工具网关的正常审批：Ask 需要确认，完全自动遵循现有规则；consult 不提供电脑工具。审批后重新读取电脑配置，用户在等待审批期间关闭或更换电脑会使调用失败，避免旧审批访问新目标。Bot 无法通过此工具打开宿主授权。

Bot 的截图以 MCP image content 返回，同时加入当前 assistant 消息的附件。单张截图最多 8 MiB，仅接受合法 PNG/JPEG 并限制像素尺寸；历史最多保留最新四张、合计不超过 12 MiB base64 数据，避免超过转录行限制。面板预览默认不保存。

## 协议与接口

Boat 使用公开 v1 API 的 `POST /sandboxes/{id}/commands` 与 `GET /sandboxes/{id}/artifacts`。命令只由受限动作生成，文本进行 shell 参数引用；截图使用唯一临时文件并在读取后清理。不重试输入动作，避免丢失响应时重复点击或输入。

Cua 使用标准 MCP initialize 和 tools/call，调用 `get_desktop_state`、`click`、`type_text`、`press_key`、`scroll`，屏幕操作作用域为 desktop。截图要求 Cua 返回 MCP image content。不同 Cua 版本若仅返回文件路径或 JSON 文本截图，会明确报错，不会读取返回的任意本机路径。

内置驱动由 GUI 进程直接启动，使用私有 0700 临时目录和 0600 Unix socket；内部仍使用 MCP，但用户无需部署 MCP 服务。驱动以 standard 权限模式运行，不继承凭据或权限绕过环境变量，不自动下载或重放失败操作。父进程保活管道关闭时驱动退出；停止也会取消正在执行的调用。驱动重启会使旧轮次的待批操作失效。

独立 `bqbot serve` 不提供内置本机控制；桌面附着到独立服务时需要退出该服务并从桌面启动。开发运行先执行 `python3 -I scripts/prepare-cua.py`，开发驱动校验固定 SHA-256，安装包校验应用签名。完整打包与许可信息见 [发布说明](RELEASE.md)。

本地管理接口沿用主服务鉴权：

| 接口 | 用途 |
|---|---|
| `GET /v1/computer-host` | 驱动可用性、运行状态及系统权限 |
| `POST /v1/computer-host/start` | 显式启动内置驱动 |
| `POST /v1/computer-host/stop` | 停止驱动并取消调用 |
| `GET /v1/computers` | 读取非秘密连接配置 |
| `PUT /v1/computers` | 保存配置及显式启用状态 |
| `POST /v1/computers/{bot}/action` | 用户操作；非截图动作要求 `confirmed: true` |

桌面通过 `Desktop.Computer` 的固定命令访问，不向页面暴露服务 token。

## 验证与边界

定向测试覆盖 Boat 命令、截图下载及临时文件清理、输入引用、Cua MCP 图像、宿主显式授权、关闭后拒绝、动作与图片验证，以及 Bot 审批前不连接、审批期间配置撤销、consult 无工具。适配器和审批测试通过竞态检测。

定向测试另覆盖内置驱动并发启动合并、启动中停止、退出清理、停止取消调用、重启使旧审批失效、宿主身份拒绝、环境隔离、无界面服务拒绝本机控制、旧 HTTP 配置显式迁移及打包归档篡改/路径校验。真实 WebView 已验证驱动状态与缺少权限时拒绝启动；实际 0.28.2 二进制已验证 MCP 握手、只读权限查询、私有 socket 和父进程保活退出。完整 `.app` 已构建并通过签名校验。

当前环境没有授予屏幕录制和辅助功能权限，未验证真实截图或鼠标键盘输入，也未自动申请权限。Boat 账号、真实 VM、正式签名公证和 macOS 13 实机兼容性仍需验收。应用不管理 VM 生命周期。

协议依据：

- [Boat Public API v1 OpenAPI](https://docs.boat.dev/openapi/boat-v1.yaml)
- [Cua Driver 0.28.2 嵌入协议](https://github.com/trycua/cua/blob/cua-driver-rs-v0.28.2/libs/cua-driver/rust/Skills/cua-driver/EMBEDDING.md)
