# Repository Guidelines

## Project Structure & Module Organization

bqbot is a Go desktop chat application using MyGo's native UI and local agent CLIs. Root files contain startup (`main.go`), UI (`view.go`), theme, icons, and shell PATH handling. Backend packages live in `internal/`: `driver` adapts Claude, Codex, Pi, and the offline Echo engine; `harness` coordinates turns and bot handoffs; `mcp` implements the stdio tool server; `store` persists bots and transcripts. Tests sit beside source files as `*_test.go`. `docs/PLAN.md` describes architecture and planned features; distinguish that roadmap from implemented behavior. `mygo.json` configures packaging; generated bundles and screenshots belong in ignored `build/`.

## Build, Test, and Development Commands

Use the Go version declared in `go.mod` (currently 1.27.3).

- `go run .`: launch the native desktop app.
- `go build -o build/bqbot .`: compile the application binary.
- `mygo build`: package the application using an installed MyGo CLI and `mygo.json`.
- `go test ./internal/store`: run one affected package's tests; substitute `driver`, `harness`, or `mcp` as needed.
- `go test . -run TestCreateChatDelete`: run a focused UI behavior test.
- `mkdir -p build/shots` followed by `BQ_FRAMES=build/shots go test . -run TestChatFrames`: generate light/dark UI screenshots.

## Coding Style & Naming Conventions

Format changed Go files with `gofmt` (tab indentation). Follow existing lowercase package names, exported `CamelCase` identifiers, and unexported `camelCase` identifiers. Keep engine adapters in separate files under `internal/driver`. Preserve package boundaries and document exported behavior. No additional formatter or linter configuration is checked in.

## Testing Guidelines

Use Go's `testing` package and `TestXxx` names; UI tests use MyGo's `ui.NewTester`. Prefer temporary stores (`t.TempDir()`) and Echo or fake drivers for isolated tests. Cover changed behavior and relevant failure paths. Run only necessary module or targeted tests during development, not the full suite. No numerical coverage threshold is configured.

## Commit & Pull Request Guidelines

Existing history uses short English imperative subjects, without an established prefix scheme. Current repository instructions require Chinese commit messages and prohibit creating commits without an explicit user request. PRs should describe the behavior change, relevant issues, and focused validation; include light/dark screenshots for visual changes.

## Configuration & Security

Data defaults to `~/.bqbot`; use `BQBOT_HOME` for an isolated development store. Keep credentials, transcripts, and generated artifacts out of Git. Preserve restricted engine permissions and per-turn MCP grant files with mode `0600`; never expose grant tokens in logs or command arguments.
