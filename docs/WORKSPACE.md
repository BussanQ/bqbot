# 内置 bqbot 与 Linux 工作区

在新安装中打开 bqbot 就能看到默认伙伴 **bqbot**。它使用内置的流式模型客户端，可以在同一轮对话中调用获准的 MCP 工具并读取结果。升级已有工作区时会添加这位伙伴，保留已有伙伴和聊天记录。如果已经有同名伙伴，应用会沿用它。之后改名或删除也会保留。

## 接入模型

打开 bqbot 对话右上角的「模型服务」，填写支持 Chat Completions 流式响应及 function calling 的服务地址、密钥环境变量名和模型 ID。可使用本地服务。使用官方默认地址时会读取 `OPENAI_API_KEY`，自定义服务只读取明确指定的变量。设置环境变量后需从能继承这些变量的环境启动 bqbot 服务或桌面应用。

bqbot 沿用现有对话记忆、转交和工具审批。内置伙伴还会读取当前工作目录中可选的 `AGENTS.md`（最大 32 KiB）。私人对话的默认目录是 `~/.bqbot/work/<bot-id>/`，频道使用频道目录。这与 Linux 容器中的桌面文件是两个位置。

## 启动一台工作区

从仓库根目录执行以下命令，需要 Go、Docker 和 Docker Compose。令牌应保存在自己的凭据管理器中，在重建工作区或启动桌面客户端时使用同一个值。

```sh
export BQBOT_WORKSPACE_TOKEN="$(openssl rand -hex 32)"
./workspace/build.sh
docker compose -p bqbot-desktop -f workspace/compose.yaml up --build -d
go run .
```

如果本机已有 `bqbot-workspace:local` 镜像，可以直接用 `docker compose -p bqbot-desktop -f workspace/compose.yaml up --no-build --pull never -d` 启动。

在 bqbot 对话输入区点电脑图标，选择「Linux 工作区（Workspace Bridge）」。地址填 `http://127.0.0.1:18443`，凭据环境变量名填 `BQBOT_WORKSPACE_TOKEN`，勾选「启用此电脑」后保存。再次点电脑图标即可观看实时画面。内置 bqbot 和已有 CLI 伙伴可以调用工作区工具。OpenAI 兼容 API 和 Echo 伙伴也可以供用户打开电脑面板。

Xvnc 提供一台 1280×800 Linux 桌面。Chromium 的配置与桌面的 `/home/agent/workspace` 分别存放在 Compose 命名卷中。运行 `docker compose -p bqbot-desktop -f workspace/compose.yaml down` 停止实例并保留数据。使用相同项目名重新启动会继续使用这两个卷。为另一位伙伴建立独立桌面时，使用不同的项目名、令牌、Bridge 和媒体端口。跨架构构建客户端可通过 `TARGETARCH=arm64` 或 `TARGETARCH=amd64` 选择工作区所在机器的架构。

## 操作桌面与浏览器

- **实时画面**使用 WebRTC 的 VP8 视频。关闭面板会结束视频会话，禁用或更换该伙伴的电脑会撤销观看连接。媒体端口不可达时，面板会提示原因并切换为截图预览。
- **坐标操作**需先勾选面板的输入开关。可以点击视频或截图、输入文字、发送按键和滚动。点击和截图使用 Xvnc 的 RFB framebuffer。
- **浏览器操作**可以选择标签页、打开网址、读取页面、选择网页元素、点击或填写。操作通过本机 Chromium 的 CDP 连接执行。**辅助功能**可以读取 AT-SPI 树，从列表中选择并激活桌面控件。找不到原控件时重新读取即可。
- **Agent 操作**使用既有的 `external_computer` 工具。浏览器支持 `browser_tabs`、`browser_open`、`browser_read`、`browser_click`、`browser_fill`。GUI 支持 `gui_tree`、`gui_invoke`，以及截图、坐标、文字、按键和滚动。每次操作经过原有 MCP 审批，consult 模式没有电脑工具。可以在聊天中试用「打开这个网址，读取页面标题，再截一张图」。

## 远程部署

默认 Compose 只把 Bridge 的 TCP 18443 和 WebRTC 的 UDP 18444 发布到宿主机回环接口。Linux 工作区内部的 RFB 5901 和 CDP 9222 只监听容器回环。远程工作区可部署在 Docker 宿主机或 VPN 内。

| 配置 | 用途 |
| --- | --- |
| `BQBOT_BRIDGE_PORT`、`BQBOT_BIND_ADDR` | 选择 Bridge 的宿主机端口和绑定地址。通过 HTTPS 反向代理或者 SSH 转发到客户端回环 HTTP 后，把相应根地址填入 bqbot。 |
| `BQBOT_MEDIA_PORT`、`BQBOT_MEDIA_BIND_ADDR` | 选择 WebRTC 使用的固定 UDP 端口及宿主机绑定地址，在网络中放行这个 UDP 端口。 |
| `BQBOT_ICE_HOST` | 填客户端能直接到达的宿主机 IPv4 地址，如 VPN 地址。同机 Docker Desktop 使用默认的 `127.0.0.1`。 |

Bridge 使用 Bearer 令牌。供其他 MCP 客户端使用的 Streamable HTTP 地址是 `<Bridge 根地址>/mcp`，工具名为 `workspace_action`。给其他客户端 Bridge 令牌会直接授予其操作这台工作区的能力。bqbot 自身的 Agent 只收到逐轮授权，页面只收到 WebRTC 会话描述。手机聊天配对继续使用原来的聊天权限。

此版本使用直连 IPv4 UDP，没有部署 TURN 中继。只转发 HTTP 即可继续使用 MCP 操作和截图，实时视频还需要媒体端口可达。镜像中的 Chromium 以普通用户运行并为容器兼容性关闭 Chromium 自身的 sandbox。隔离边界为该伙伴的 Linux 容器。Compose 只挂载工作区专用卷。

工作区启动后可用下列只读检查验证 RFB 截图、Chromium 标签页和 AT-SPI 应用树。

```sh
BQBOT_TEST_WORKSPACE_URL=http://127.0.0.1:18443 go test ./internal/computer -run '^TestLiveWorkspaceProtocols$' -count=1
```

内置 Agent 和持久工作区参考了 [Memoh](https://github.com/felinics/Memoh) 及其 [工作区模板](https://github.com/felinics/Memoh/tree/main/templates/workspace)。本仓库的 Bridge、Agent 循环和桌面适配器在 bqbot 中实现。
