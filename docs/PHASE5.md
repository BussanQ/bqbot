# 阶段 5：伙伴的电脑

电脑入口位于桌面设置中的「伙伴的电脑」。每个 Bot 可以配置一台已有桌面：Boat 云 Sandbox、运行 Cua Driver 的 VM，或者这台宿主机。连接默认关闭；访问宿主机还必须单独勾选允许屏幕和输入设备访问。

## 配置与使用

- **Boat**：API 地址使用 `https://boat.dev/api/v1`，填写已有的 Sandbox ID 和 API token 的环境变量名。Sandbox 必须处于可用状态，运行 X11 桌面并安装 `scrot`、`xdotool`。bqbot 不自动创建、收费、恢复或销毁云机器。
- **VM**：填写已有 Cua Driver 的 Streamable HTTP MCP 地址；可以使用 HTTPS，或通过 SSH 端口转发使用字面量回环 HTTP 地址。VM 的创建、启动和系统安装由现有虚拟化软件负责。
- **宿主机**：使用 Cua Driver 的本机 MCP 地址（如 `http://127.0.0.1:端口/mcp`），显式开启宿主授权。Cua Driver 仍需要操作系统允许屏幕录制和辅助功能控制。

凭据只保存环境变量名，值在 Go 中读取。配置写入 `computers.json`（0600）。远程地址不允许 URL 用户信息、查询串或 fragment；HTTP 仅允许字面量回环地址，宿主类型始终限制回环地址。

打开电脑后，面板每三秒获取一次截图。关闭面板或离开面板后停止刷新。勾选面板的输入授权后，可点击画面、输入文字、按 Enter/Escape 和上下滚动。「发送截图到当前对话」重新获取截图并作为用户图片消息发送，当前对话必须属于该 Bot 且是开放的私人线程。图片附件名以 `computer-` 开头，聊天界面将其折叠显示。

## Bot 工具与审批

为支持 MCP 的引擎启用电脑后，后续轮次出现 `external_computer` 工具，支持 `screenshot`、`click`、`type`、`key`、`scroll`。工具没有任意 shell、文件读写或服务器配置入口。

每次调用复用外部工具网关的正常审批：Ask 需要确认，完全自动遵循现有规则；consult 不提供电脑工具。审批后重新读取电脑配置，用户在等待审批期间关闭或更换电脑会使调用失败，避免旧审批访问新目标。Bot 无法通过此工具打开宿主授权。

Bot 的截图以 MCP image content 返回，同时加入当前 assistant 消息的附件。单张截图最多 8 MiB，仅接受合法 PNG/JPEG 并限制像素尺寸；历史最多保留最新四张、合计不超过 12 MiB base64 数据，避免超过转录行限制。面板预览默认不保存。

## 协议与接口

Boat 使用公开 v1 API 的 `POST /sandboxes/{id}/commands` 与 `GET /sandboxes/{id}/artifacts`。命令只由受限动作生成，文本进行 shell 参数引用；截图使用唯一临时文件并在读取后清理。不重试输入动作，避免丢失响应时重复点击或输入。

Cua 使用标准 MCP initialize 和 tools/call，调用 `get_desktop_state`、`click`、`type_text`、`press_key`、`scroll`，屏幕操作作用域为 desktop。截图要求 Cua 返回 MCP image content。不同 Cua 版本若仅返回文件路径或 JSON 文本截图，会明确报错，不会读取返回的任意本机路径。

本地管理接口沿用主服务鉴权：

| 接口 | 用途 |
|---|---|
| `GET /v1/computers` | 读取非秘密连接配置 |
| `PUT /v1/computers` | 保存配置及显式启用状态 |
| `POST /v1/computers/{bot}/action` | 用户操作；非截图动作要求 `confirmed: true` |

桌面通过 `Desktop.Computer` 的固定命令访问，不向页面暴露服务 token。

## 验证与边界

定向测试覆盖 Boat 命令、截图下载及临时文件清理、输入引用、Cua MCP 图像、宿主显式授权、关闭后拒绝、动作与图片验证，以及 Bot 审批前不连接、审批期间配置撤销、consult 无工具。适配器和审批测试通过竞态检测。

Boat 账号及真实 Sandbox、真实 Cua Driver / VM / 系统录屏与辅助功能权限未在当前开发环境实测；测试使用本机模拟服务。此阶段提供连接既有桌面的实现，未实现 VM 生命周期管理或自动安装 Cua Driver。

协议依据：

- [Boat Public API v1 OpenAPI](https://docs.boat.dev/openapi/boat-v1.yaml)
- [Cua Driver 0.12.6 工具模式样本](https://github.com/milind-soni/OpenMausBot/blob/main/electron/fixtures/cua-driver-lent-tool-schemas.json)
