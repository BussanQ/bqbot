# Local Harness service

`Listen(h, "")` starts authenticated HTTP on `127.0.0.1:8799`; tests use
`127.0.0.1:0`. `NewClient(server.URL, server.Token)` implements the same protocol
used by the desktop window. Never log the token or put it in command arguments.
`Server.Close` disconnects clients; the caller owns `Harness.Close`.

See [the phase 2 guide](../../docs/PHASE2.md) for endpoints and configuration.

## Event recovery

`GET /v1/events` first subscribes, then sends an authoritative `snapshot` array
from `Harness.FullSnapshot`. Each entry has `sync: true`, a runtime sequence,
the bot profile, persisted messages, visible queued messages, active reply and
pending approvals. Idle bots are included. Clients replace their thread state
on this snapshot, then apply `update` objects in increasing sequence order.
Events already covered by the snapshot are discarded. Two-second SSE comments
provide heartbeats without retransmitting transcripts or images.

A slow subscriber is disconnected when its bounded mailbox overflows. `Watch`
reconnects with a 250 ms–5 s backoff and obtains a fresh snapshot, including
replies completed during the outage and approval removals. A 15-second receive
watchdog detects stale connections. Authentication/protocol errors terminate
`Watch`; cancellation returns `ctx.Err()`. Messages are complete objects, not
text deltas. There is no `Last-Event-ID` replay promise; runtime history is the
harness's NDJSON journal.

## Validation and discovery

The listener only accepts loopback addresses, requires a random bearer token,
and rejects browser Origin headers and unexpected Host headers. The client
disables proxies and redirects. JSON requests are bounded to 18 MiB. Attachments
are limited to four images, 5 MiB each and 12 MiB combined, with content/MIME and
image validation. Credentials are environment-variable references, not literal
API keys. Model IDs can be custom; known models validate effort against their
catalog. Engine paths and catalogs are stored in `settings.json`.

`WriteDiscovery` atomically publishes a 0600 file in a private directory and
refuses to overwrite existing files or symlinks. `Close` removes only discovery
files whose inode and contents still match. The application startup can reclaim
a stale, recognizable discovery file after it has acquired that exact listener
address, so crash recovery rotates the token.

## Checks

```sh
go test -race ./internal/service
```

Tests run real loopback HTTP/SSE with temporary stores and fake drivers. They
exercise authentication, CRUD, settings, approval, queueing, stop, reconnection,
completed-reply recovery, cancellation, validation and discovery ownership.
