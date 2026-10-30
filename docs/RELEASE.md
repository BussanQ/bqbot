# 发布与更新

应用沿用 MyGo v0.7.1 打包；`mygo.json` 定义应用标识、版本、macOS 麦克风/语音识别说明与 Linux 桌面入口。没有分析 SDK、统计请求或设备追踪。签名更新使用 MyGo 的 Ed25519 验签和安装 UI，默认不自动检查；用户可从设置中的「检查更新」主动开启检查。

## 本地构建

```sh
go build -o build/bqbot .
go run github.com/egoist/mygo/cmd/mygo@v0.7.1 build -platform darwin/arm64
```

macOS 生成 `.app` 与 DMG；Windows 生成 EXE 安装器（需 NSIS）；Linux 生成 deb 和归档。macOS 签名、公证和 DMG 需要 macOS。默认开发配置不包含更新公钥或发布源，只生成开发构建，不能声称已经可以安全分发给其他电脑。

## 可审阅的发布流程

`.github/workflows/release.yml` 仅手动触发，输入必须是已经存在、且版本与 `mygo.json` 一致的 tag。流程不创建 tag、不提交、不发布 Release。它在 macOS 和 Linux runner 分别生成各平台签名产物并上传 Actions artifact，供维护者验收后发布。

配置 GitHub `release` environment 的变量：

- `MYGO_UPDATER_PUBLIC_KEY`：由 MyGo keygen 生成的 base64 Ed25519 公钥。
- `MACOS_SIGNING_IDENTITY`：Developer ID Application 证书名称。

配置 secrets：

- `MYGO_UPDATER_PRIVATE_KEY`：更新私钥内容。
- `MACOS_CERTIFICATE`（p12 的 base64）、`MACOS_CERTIFICATE_PASSWORD`。
- `APPLE_ID`、`APPLE_TEAM_ID`、`APPLE_APP_PASSWORD`（公证专用密码）。
- `WINDOWS_CERTIFICATE`（pfx 的 base64）、`WINDOWS_CERTIFICATE_PASSWORD`。

脚本 `scripts/release-signing.py` 将证书写到临时目录、以 0600 保存，并在结束时删除；不打印证书、密码或工具原始错误。`scripts/configure-release.mjs` 在临时 checkout 中验证版本和必要配置，生成包含公钥、发布源和签名身份的配置，不将私钥写入 JSON。发布源取当前仓库；仅适用于公开 GitHub Releases。更新密钥必须跨版本保持稳定。

验证 artifacts 后，将各平台安装包、归档、`update-*.json` 及完整的校验文件发布到同一个 `v<version>` Release。不同平台的 SHA256SUMS 需要合并；不要以最后一个平台覆盖其他平台的条目。先上传完整产物，最后公开 Release。MyGo 默认跳过草稿和 prerelease。

## 发布验收边界

本仓库可完成本地编译、模块测试和开发打包；生产签名、公证、跨机器安装与真实升级需要上述证书及已发布版本。不能用模拟测试代替这些验收。Windows、Linux 和不同架构的安装包仍需在相应系统上检查启动、音频、WebView 与卸载。首次发布前应进行浅色/深色、窄窗口和键盘交互验收，以及真实供应商连接测试。
