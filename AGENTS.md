# Repository Guidelines

## Project Structure & Module Organization

bqbot is a Go desktop chat application using MyGo's system WebView and local agent CLIs. `web/` contains the embedded HTML, CSS, JavaScript, and Markdown renderer; `webgo/desktop.go` provides the Go bridge. `webgo/` groups Go bridge code, shared conversation state/reducers, window startup, and retained legacy native views by feature; `web/assets.go` embeds the frontend resources. Backend packages live in `internal/`: `driver` adapts engines; `harness` coordinates turns; `service` exposes authenticated local APIs; `mcp` implements tools; `store` persists data. Go tests sit beside sources as `*_test.go`; JavaScript tests live in `tests/`. `docs/PLAN.md` is the roadmap; `docs/WEBVIEW.md` describes the desktop frontend. Packaging uses `mygo.json`; generated artifacts belong in ignored `build/`.

## Build, Test, and Development Commands

Use the Go version declared in `go.mod` (currently 1.27.3).

- `go run .`: launch the native desktop app.
- `go build -o build/bqbot .`: compile the application binary.
- `mygo build`: package the application using an installed MyGo CLI and `mygo.json`.
- `go test ./internal/store`: run one affected package's tests; substitute `driver`, `harness`, or `mcp` as needed.
- `go test ./webgo -run 'TestDesktop|TestWebFrontend'`: test the WebView bridge and embedded assets.
- `node --test tests/markdown.test.mjs`: check Markdown rendering and HTML escaping.
- `go test ./webgo -run TestCreateChatDelete`: run a legacy native UI behavior test.

## Coding Style & Naming Conventions

Format Go files with `gofmt` (tabs); use two-space indentation in frontend files. Follow lowercase package names, exported `CamelCase` identifiers, and unexported `camelCase` identifiers. Keep engine adapters in `internal/driver`. Preserve package boundaries and document exported behavior. No linter configuration is checked in.

## Testing Guidelines

Use Go's `testing` package and `TestXxx` names; UI tests use MyGo's `ui.NewTester`. Prefer temporary stores (`t.TempDir()`) and Echo or fake drivers for isolated tests. Cover changed behavior and relevant failure paths. Run only necessary module or targeted tests during development, not the full suite. No numerical coverage threshold is configured.

## Commit & Pull Request Guidelines

Existing history uses short English imperative subjects, without an established prefix scheme. Current repository instructions require Chinese commit messages and prohibit creating commits without an explicit user request. PRs should describe the behavior change, relevant issues, and focused validation; include light/dark screenshots for visual changes.

## Configuration & Security

Data defaults to `~/.bqbot`; use `BQBOT_HOME` for an isolated development store. Keep credentials, transcripts, and generated artifacts out of Git. Preserve restricted engine permissions and per-turn MCP grant files with mode `0600`; never expose grant tokens in logs or command arguments.
