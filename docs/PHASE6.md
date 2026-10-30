# 阶段 6：语音

桌面语音设置提供五种合成方式、朗读最近回复、自动朗读当前对话的新回复、听写到输入框，以及轮流听写/回复/朗读的通话模式。默认不启用合成或自动朗读，也不会自动打开麦克风。云端合成会把朗读文本发送给所选提供商。

## 配置和使用

在引擎设置的语音入口选择提供商并保存：

| 提供商 | 配置与行为 |
|---|---|
| macOS `say` | 可选已安装的声音名称；通过标准输入传递文本，生成临时 WAV 后由 `mygo/audio` 播放。不会在命令参数中放置文本；临时目录在合成完成或取消后删除。仅 macOS 可用。 |
| ElevenLabs | 声音 ID 必填，密钥填写环境变量名；默认 `eleven_multilingual_v2`，请求 24 kHz PCM 后封装为 WAV。 |
| Fish Audio | 可选 reference ID，默认模型 `s1`；请求 WAV，模型通过 `model` 请求头设置。 |
| xAI | 默认声音 `eve`，请求 24 kHz PCM；语言可留空使用 `auto`。 |
| Chatterbox | 另行安装并运行支持 OpenAI 兼容 `POST /v1/audio/speech` 的 Chatterbox 服务。仅接受 `http://127.0.0.1:端口` 或其他字面量 loopback 地址，默认模型 `chatterbox`、声音 `default`、WAV 输出。本项目不下载模型或启动推理服务。 |

凭据只保存环境变量名称。服务从自身进程环境中读取其值，因此启动应用的环境需要包含该变量。配置文件为数据目录中的 `voice.json`，权限 `0600`；配置状态只显示密钥是否设置，不回传其值。URL 不允许用户信息、查询参数或 fragment。云提供商地址固定，所有合成请求拒绝重定向。

自动朗读只处理当前对话中新增且完成的回复，不朗读加载的历史记录或流式片段。切换对话、保存设置、结束通话、关闭窗口会停止语音。合成和播放可取消；新的朗读请求替换旧请求。

## 听写与通话

听写使用系统 WebView 提供的 `SpeechRecognition` 或 `webkitSpeechRecognition`。不支持时按钮禁用，可使用系统键盘听写输入文字。识别服务由操作系统/WebView 决定，可能联网；应用不会假称这是本地离线识别。

“听写到输入框”只填写草稿。“开始通话”明确打开麦克风，并把最终识别结果发送到开始识别时选定的对话；切换对话会停止。Bot 完成回复后合成并朗读，播放结束再开始下一次识别，避免把应用自己的声音重新发给 Bot。静默导致识别结束且没有消息时停止通话，需要再次点击启动。通话不是全双工实时音频，也不提供打断中的回声消除。

系统 WebView 必须支持语音识别并获得麦克风权限；macOS 签名包还需要相应的隐私用途说明。这部分依赖宿主与打包环境，尚未完成真实麦克风验收。

## 接口

| 接口 | 行为 |
|---|---|
| `GET /v1/voice` | 公共设置与 `configured` 标记 |
| `PUT /v1/voice` | 校验并保存设置 |
| `POST /v1/voice/synthesize` | JSON `{"text":"…"}`，返回 WAV；不自动播放 |
| `Desktop.Voice` | `settings`、`save`、`speak`、`stop`、`status` 的封闭命令集合 |

语音接口使用现有本地服务鉴权，不作为 Bot 的 MCP 工具公开。播放由桌面持有的 `voice.Player` 管理。单次文本最多 5,000 个 Unicode 字符，响应音频最多 16 MiB，合成请求 40 秒超时。播放器拒绝超过 10 分钟或不支持声道/采样率的音频。错误只回传状态，不包含提供商响应正文或请求凭据。

## 验证记录与边界

针对性测试覆盖：四个 HTTP 提供商请求字段、鉴权位置、PCM/WAV 解码、错误正文不泄密、禁用和长度限制、停止与替换请求取消、关闭播放器、配置读写/权限/凭据不持久化，以及本地模拟 Chatterbox 端到端响应。Go 相关测试通过 `-race`；前端测试覆盖不朗读历史或流式片段、每条完成回复只朗读一次、切换对话停止和缺失识别能力的明确错误。

真实 WebView 已验证单条回复朗读：macOS `say` 合成、`mygo/audio` 播放状态到完成，无设备错误。外部账号调用、真实 Chatterbox 推理、实际听感、麦克风权限与不同 WebView 的真实听写交互仍需对应环境验收。协议适配成功不等同于这些外部验证已经通过。

协议依据：

- [ElevenLabs Create speech](https://elevenlabs.io/docs/api-reference/text-to-speech/convert)
- [ElevenLabs 音频格式](https://elevenlabs.io/docs/help-center/troubleshooting/what-audio-formats-do-you-support)
- [Fish Audio 开发者接口](https://fish.audio/developers/)
- [Fish Audio Go SDK](https://github.com/fishaudio/fish-audio-go/blob/main/tts.go)
- [xAI Text to Speech](https://docs.x.ai/developers/model-capabilities/audio/text-to-speech)
