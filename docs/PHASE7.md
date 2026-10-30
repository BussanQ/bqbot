# 阶段 7：团队分享与无界面运行

团队、CLI 和受限控制面已实现；远程访问的配置与边界见远程访问说明。所有 CLI 命令复用正在运行的桌面或 `bqbot serve`，从 `BQBOT_HOME/service.json` 读取私有服务发现文件，不在命令行传递 bearer token。

## 团队 Markdown

桌面设置的「团队与分享」提供 Markdown 导入预览、预设团队和全团队/部门导出。部门指一个频道及其成员；部门导出只包含该频道、成员与成员的自动任务。

```markdown
---
version: 1
name: 文档组
bots:
  - name: 文档伙伴
    engine: echo
    persona: |
      帮助整理文档，明确说明未验证的事项。
rooms:
  - name: 文档协作
    members: [文档伙伴]
    lead: 文档伙伴
    reply_mode: lead
routines:
  - name: 每小时整理
    bot: 文档伙伴
    kind: interval
    interval_seconds: 3600
    prompt: 整理待办事项。
---

可选的团队说明。
```

YAML 严格拒绝未知字段，最大文件为 2 MiB，最多 32 个伙伴、32 个频道、128 个自动任务。姓名引用必须唯一且指向本文件内的伙伴。导入先验证所有内容和引擎，再生成新的本地 ID。存在同名伙伴或频道时拒绝导入，避免覆盖现有会话。

导入的伙伴固定使用 Ask 权限，自动任务一律暂停，日程下次时间留空。导入不会运行人物指令或任务。正常写入失败会删除本次新建对象并返回错误；回滚失败会明确报告。跨多个 JSON 文件的导入不是崩溃原子事务：进程在写入中途被强制终止时可能留下部分新建对象，可以按导入姓名检查后删除或重命名。

导出只构造显式允许的分享字段，不包含本地 ID、会话游标、转交游标、权限设置、凭据、引擎配置、MCP 环境配置、工作目录、聊天记录、记忆或自动任务运行历史。人物指令、频道指令、任务文本原样保留，无法识别用户手动写进自由文本的秘密，分享前应检查预览。

预设包含「离线演示」和「研发小组」。预设也是普通导入，绝不自动安装 CLI、运行命令或开启自动化。

## CLI

先启动服务：

```sh
BQBOT_HOME=/path/to/private-data bqbot serve
# 多实例或端口冲突时可指定 --listen 127.0.0.1:0，CLI 从 service.json 读取地址。
```

另一个终端使用相同的 `BQBOT_HOME`：

```sh
bqbot team presets
bqbot team apply --preset 离线演示
bqbot team apply team.md
bqbot team apply - < team.md
bqbot team export --name 我的团队 --output team.md
bqbot team export --room ROOM_ID --output department.md
bqbot run --bot Echo --prompt '检查本次运行' --json --export result.json
bqbot run --bot BOT_ID --timeout 10m --prompt - < task.txt
```

`team export` 未指定输出文件时将 Markdown 写到 stdout。导出文件和 `run --export` 结果使用 `0600`，拒绝覆盖已有路径。`--json` 将最终结果作为单个 JSON 对象输出到 stdout，诊断信息进入 stderr；不会混入日志或进度文字。CLI 参数遵循 Go flag 规则：选项放在位置参数之前。

每次 `run` 都创建独立线程，复用 Bot FIFO、引擎和已有权限。线程在运行完成、出错、取消或超时后关闭，聊天记录仍然保留。等待结果按本次线程识别，并等待其协调链与自动续答完成；不会误取主会话历史或其他线程的回复。默认超时 5 分钟，最大 24 小时，Ctrl-C 取消本次线程及关联协调任务。

无人值守运行不会自行回答审批。遇到主轮次或关联委派的审批卡片时，返回 `approval_required` 并取消本次工作；可在桌面调整 Bot 权限后显式重试。已配置的自动编辑/完全自动模式继续沿用现有审批策略。

| 退出码 | 含义 |
|---|---|
| 0 | 运行或导入导出成功 |
| 1 | 引擎、连接、存储或任务失败 |
| 2 | 命令用法或本地输入格式不正确 |
| 3 | 本次运行需要交互审批 |
| 124 | 运行超时 |
| 130 | 运行被取消 |

结果 JSON 包含 `thread_id`、`user_id`、`status`、`text`、可选 `error` 和本次线程的 `messages`。结果文件包含用户任务及回复，请按聊天记录保护。

## 受限 stdio MCP 控制面

```sh
bqbot control
```

通过 newline-delimited JSON-RPC 提供 MCP 初始化、ping、工具发现和工具调用，只允许：

- `list_bots`：ID、名称和引擎。
- `send_message(bot_id, text)`：按现有 Bot 权限排队，不等待最终结果。
- `status`：线程忙闲、队列长度及是否等待审批，不返回审批参数。
- `transcript(bot_id)`：指定 Bot 的主会话内容。

没有审批、删除、配置、凭据、文件或通用 HTTP 工具。`control` 应只交给你信任、且允许查看聊天和向 Bot 发消息的本机 MCP 客户端；它不是面向陌生调用者的匿名服务。

## 接口与验证

本地认证服务新增 `/v1/team/presets`、`/v1/team/preview`、`/v1/team/apply`、`/v1/team/export` 与 `/v1/run`。`/v1/run` 为阻塞请求，调用方断开会取消任务；CLI 使用可取消的长请求连接。

定向测试覆盖 Markdown 往返、未知权限字段拒绝、引用验证、导出秘密字段排除、冲突不覆盖、写入前验证、运行线程隔离、审批退出、超时取消、JSON 输出、私有导出文件与覆盖拒绝，以及 MCP 工具白名单和列表元数据过滤。测试用 Echo 或模拟驱动，不包含真实提供商账户的在线调用。真实 CLI 已验证隔离服务、团队导入/导出、`run --json --export` 和 `control` MCP 调用。桌面团队入口已验证；文件选择与原生保存对话框仍需人工验收。导出还可使用「复制内容」。
