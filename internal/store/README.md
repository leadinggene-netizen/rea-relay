# internal/store — durable, file-backed persistence

The relay's long-lived state used to live only in RAM, so a restart wiped
everything. This package makes that state durable on the local filesystem. It
uses the **Go standard library only** — no SQLite, no `database/sql` driver, no
third-party package — because the Go module proxy is unavailable in the
build/runtime environment and `go.mod` must stay dependency-free. Persistence is
therefore file-backed (JSON snapshots + append-only JSONL), not a SQL database.

## File layout

Everything lives under the `-data-dir` directory (default `./data`):

| File          | Format                | Contents                                                     |
|---------------|-----------------------|--------------------------------------------------------------|
| `config.json` | JSON object           | Subscriptions + connections (the registry snapshot).         |
| `dedup.json`  | JSON object           | Idempotency map: `"<connID>\x00<key>" → first-seen RFC3339`. |
| `audit.jsonl` | JSON Lines (append)   | One audit event per line (`{connection_id,code,detail,at}`). |

A missing file is treated as empty, so a fresh data dir is valid and loads as
empty state (this is what the seed-on-empty logic keys off).

## What is persisted (and what is not)

**Persisted:**

- **Subscriptions & connections** (`internal/connection`): id, key,
  tier/max-connections, symbol table, accounts, expiry. Loaded on startup;
  written through on every change via `connection.Store.SetPersister`.
- **Idempotency / dedup keys** (FR-4): the 5-minute dedup set. Surviving entries
  are reloaded on startup so a webhook replayed within the window is **not**
  re-executed after a reboot. Entries older than the window are pruned on load
  and by a periodic prune.
- **Audit events** (§8): appended to `audit.jsonl`, giving the operator a
  durable, append-only operational record.

**Deliberately NOT persisted — in-flight delivery:** the long-poll / WebSocket
delivery queue (orders mid-flight, buffered-while-offline, sent-but-unacked) is
**not** written to disk. On restart those are simply lost. This is acceptable
because (a) idempotency prevents double-execution of anything that was already
dispatched, and (b) the upstream (TradingView) re-fires the alert. The live
"connected agents" set used for concurrency limits is likewise in-memory only
and is rebuilt as agents reconnect.

## Atomic-write approach

`config.json` and `dedup.json` are rewritten in full on each change using a
**write-temp-then-rename** strategy (`writeAtomic`): the new content is written
to a temp file in the same directory, `fsync`'d, closed, then `os.Rename`'d over
the target. `os.Rename` is atomic within a directory, so a concurrent or
post-crash reader sees either the complete old file or the complete new one,
never a torn/partial write.

`audit.jsonl` is opened with `O_APPEND` and each line is `fsync`'d after writing,
so a record is durable even if the process dies immediately afterward.

All operations are guarded by a single `sync.Mutex` — correctness over
throughput, which suits a development/PoC relay. `dedup.json` is rewritten whole
on each fresh key; within a window each unique key is recorded at most once, so
the rewrite cost is bounded by the number of distinct keys in the window.

## Wiring

`cmd/relay/main.go` opens the store, loads the config, seeds the demo connection
only when the data dir has no connections yet, then attaches the store as a
write-through persister to the connection registry, the relay's deduper
(`Relay.AttachDedupStore`) and the audit log (`AuditLog.SetPersister`). A
background goroutine calls `PruneDedup` every dedup window.

The persister interfaces are declared in the *consumer* packages
(`connection.ConfigPersister`, `auth.DedupPersister`, `relay.AuditPersister`) so
the dependency only ever points one way: `store` imports `connection`, never the
reverse. A `*store.FileStore` satisfies all three at once and the `store.Store`
interface documents the full contract.
