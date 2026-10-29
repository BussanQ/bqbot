# Phase 2 integration

For the current thread, room, asynchronous handoff and event contract, see
[Phase 3 runtime integration](PHASE3_RUNTIME.md). The following records Phase 2
behavior; Phase 3 supersedes its hidden handoff projection and log routing.

The service API is `Subscribe`, `Snapshot`, `Submit`, `Send`, `Approve`,
`Stop`, `DeleteBot`, and `Bot`. Set `Drivers` and legacy `OnUpdate` before
submitting work. Concurrent consumers should use `Subscribe` and always invoke
its idempotent unsubscribe function. Each subscriber has a bounded independent
mailbox; slow consumers are disconnected and recover with `FullSnapshot`.
`Close` cancels and awaits
work, revokes grants, shuts down the endpoint and closes subscriptions.

`Submit(bot, text, attachments, "queue")` and `Send` use one FIFO per bot,
shared with handoffs. Messages are accepted with a stable ID; pending messages
enter the transcript only when execution starts. `Update.User` then inserts the
message (clients should upsert by ID). `Queued` is the pending count. The
additional `QueuedUser` field records admission separately from insertion.
Stopping clears pending work; deleting also awaits active work involving the bot
before removing its data. Drivers must honor their context cancellation.

Steering uses `TurnInput.Steer`. A driver opts in by implementing
`SupportsSteering() bool`. Unsupported steering, attachments, a full steer
buffer, or existing queued work falls back to FIFO. A supported text steer is
persisted before being delivered to the current turn.

Approvals are single-use within a turn. Empty or unrecognized modes become
`ask`. `auto-edit` recognizes only exact local Edit, Write, MultiEdit and
apply_patch tool names (case insensitive); other tools require UI approval.
`full-auto` allows tools, while questions always require UI answers. Handoffs
inherit the caller's mode. `Snapshot` includes each active turn and a separate
update for each pending approval. `FullSnapshot` also includes idle bots,
transcripts and visible pending messages, with a runtime sequence for rejecting
older buffered events. `Error` is the wire error; `Err` is omitted
from JSON.

Call `Listen(executable)` to provide MCP grants. CLI turns at every depth receive
`TurnInput.ApprovalMCP`; only depth zero receives `TurnInput.MCP`. The stateless
OpenAI driver does not receive tool grants. Approval grant
files contain `url`, `token`, and `approval_only: true`, with mode 0600. The
MCP owner must recognize that field and send approval requests to `POST /approve`
as `driver.ApprovalRequest`, authenticated with the grant's bearer token. Success
returns `driver.ApprovalDecision` directly. Approval-only tokens cannot call
`/list_bots` or `/ask_bot`; ordinary handoff tokens cannot request approvals.

Settings are stored atomically in `settings.json`. Runtime updates are fsynced
before publication to `events/<bot>.ndjson`, or
`events/handoffs/<target>/<caller>.ndjson`. Each line is a `store.RuntimeEvent`
with `id`, `at`, and `data` (the full Update). Driver events and approval decisions
are included in `Event` and `Decision`; handoff text is removed from broadcast
updates but retained in the handoff log. Store APIs are `AppendEvent` and
`Events`. If persistence fails, the affected turn is canceled and an explicit
error notification is published; that failure notice cannot itself be guaranteed
durable on failing storage. Runtime logs are an audit record, not automatic
restart of queued tasks after a process crash.

All turns receive prior transcript History, current Settings, Effort and image
attachments. ErrSessionExpired retries once only before text, tools or approval
requests have occurred; the retry clears the cursor and excludes the current
message from History. Engine/model edits invalidate cursors, and cursor saves
compare the engine, model and cursor atomically to avoid overwriting concurrent
session changes. Profile edits preserve service-owned continuation cursors.

Images must be valid base64 PNG, JPEG, GIF or WebP matching their MIME, at most
8 images, 6 MiB per image, 12 MiB total, and 40 million pixels per image. Validation
includes decoding before admission.

Focused validation: `go test -race ./internal/harness ./internal/store`.
