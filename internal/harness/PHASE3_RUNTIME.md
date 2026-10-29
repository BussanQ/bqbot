# Phase 3 runtime

The harness schedules private threads, shared channels and asynchronous handoffs.
The desktop UI accesses them through the authenticated HTTP/SSE service.

## Scheduling and lifecycle

Each bot retains one FIFO across main, named, handoff, and channel turns.
Approvals remain addressed by bot ID. Named/private handoff conversations use
independent store thread cursors. Channel turns replay the shared transcript
with speaker attribution, channel instructions, room memory, and room workdir;
they deliberately start a fresh provider context to include other speakers.
Private-thread summaries replace the summarized history prefix. The full
recovery context remains in TurnInput.History; existing drivers replay it only
when Cursor is empty (including session-expiry recovery).

MCP `ask_bot` dispatches asynchronously and returns JSON containing `status`,
`thread_id`, `message_id`, and `target`. It never waits for the answer. The target
inherits the caller's approval mode. Private delegation uses HandoffThread;
channel delegation stays in the current channel using RoomThread. Successful
or failed completion queues one follow-up in the original conversation. Both
delegated and automatic follow-up turns lack delegation grants, preventing
recursive wakes. A source turn may dispatch at most eight jobs. The old internal
synchronous `ask` helper remains available but is not the MCP endpoint.

Explicit stop cancels active/queued work and its dependent jobs. Close and
delete collect completion channels before cancellation, reject admission, then
await all affected workers before mutating storage. Canceled jobs remain
tracked until workers acknowledge cancellation. Removing a room member cancels
its queued/running room work and related handoffs; prepare rechecks membership.
In-flight Auto HTTP requests are canceled by stop/edit/delete/shutdown and
cannot admit work afterward. Auto chooses an exact eligible member ID from a
bounded OpenAI-compatible response and otherwise falls back to lead. Mentions
of member names or IDs take precedence and can select multiple members.

## Events and UI integration

The client methods in `internal/service/phase3_client.go` include scoped
`StopThread(id)` and `StopRoom(id)`. Old main-chat methods remain available.

- Route room events by `RoomID` first; otherwise use `ThreadID` (main equals
  `BotID`). Use `BotID` to address approvals.
- `Metadata` signals a conversation/profile/memory/settings refresh. Optional
  `Bot`, `Thread`, and `Room` contain metadata when available. Refresh after
  deletion rather than treating a metadata event as a runtime stop.
- FullSnapshot includes idle private threads, visible handoffs, and rooms.
  A room may have a base snapshot plus per-speaker snapshots for active work
  and approvals. Every projection contains all persisted and partial room
  replies; replace messages by ID and merge approvals using the speaker BotID.
- Channel messages identify speakers with `BotID`, `ThreadID`, and `From`.
  A shared user entry appears once in persisted room history; streaming events
  may repeat its ID for different target turns and must be deduplicated.
- Thread/profile/memory/room edits publish metadata events. Closed handoffs are
  preserved and a subsequent handoff creates a fresh conversation.

MCP discovery never returns continuation cursors or memory summaries. Private
grants see their own private threads and the threads they delegated to; channel
grants see only their current room. Channel ask targets must be room members.
Approval-only grants cannot call any discovery or delegation tool. The MCP
close tool rejects active scopes/dependent workers, and performs an atomic
inactive close without waiting for an engine callback. UI HTTP close can cancel
and await active work. Grant files remain 0600 and per-turn revocation is kept.
The shared `mcp.Tools` list drives the Claude/Codex/Pi whitelists, so no extra
production driver configuration changes are necessary.

## Validation

Run only the affected packages:

```
go test -race ./internal/harness ./internal/service ./internal/mcp ./internal/driver
```

Tests cover context/cursor/memory isolation and compaction, shared channel
history and mentions, local fake Auto selection/fallback, asynchronous receipt
and original-thread wake, stop/close/delete/shutdown cancellation and waiting,
member removal, canceled routing, scoped/revoked MCP grants, HTTP Echo CRUD and
snapshots, validation, credential boundaries, and the expanded CLI whitelists.
No real provider requests or dependency installs are used. Store migration and
concurrency tests live in `internal/store/chat_test.go`; root UI interactions and
light/dark screenshots are covered by `phase3_view_test.go`.
