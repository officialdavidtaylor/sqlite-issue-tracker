# SQLite Issue Tracker

`sit` is a local-first issue tracker for humans and coding agents. It keeps the
live database outside normal worktrees, gives every mutation an audit record,
and exports a clean SQLite snapshot that can be committed to Git.

## MVP capabilities

- Create, read, list, update, and soft-delete issues.
- Protect updates with optimistic `revision` checks.
- Retry mutations safely with unique `--mutation-id` values.
- Link issues with directed relationships and reject cycles in `blocks` links.
- Inspect an immutable audit history.
- Compute separate logical state and history hashes.
- Export a consistent snapshot with SQLite `VACUUM INTO` and a SHA-256 manifest.
- Share one live database between every Git worktree in a local clone.

## Install

Go 1.25 or newer is required.

```sh
go install ./cmd/sit
```

To build only inside the repository:

```sh
go build -o sit ./cmd/sit
```

## Quick start

```sh
sit init
sit create --id ISS-1 --title "Design mutation replay" --actor agent-a
sit update ISS-1 --status in_progress --expected-revision 1 --actor agent-a
sit create --id ISS-2 --title "Add sync protocol"
sit link ISS-1 ISS-2 --type blocks
sit list
sit links ISS-1
sit history ISS-1
sit snapshot
```

IDs and mutation IDs are generated when omitted. Pass a stable
`--mutation-id` when a caller may retry the same request. Machine-readable
output is available through `--json` on read commands.

By default, a Git repository uses these locations:

```text
.git/issue-tracker/live.sqlite   shared writable database, not committed
.issues/issues.sqlite           clean versionable snapshot
.issues/manifest.json           snapshot hashes and generation
```

Set `SIT_DB` or pass the global `--db PATH` option to override the live path.
Set `SIT_ACTOR` to supply the default audit actor.

## Consistency and safety

Writes use `BEGIN IMMEDIATE`, a five-second busy timeout, foreign keys, and one
serialized connection per process. Concurrent processes coordinate through
SQLite's file locking. Updates and deletes require the caller's observed
revision; stale writes fail without an audit event. Mutation IDs are unique and
replays must contain an identical request.

The checked-in database is never the live WAL database. `sit snapshot` creates
a transactionally consistent compact copy and hashes that immutable copy.
Snapshot and manifest targets are canonicalized, locked across processes,
prepared before publication, and rolled back together if installation fails.
`sit verify` runs `PRAGMA integrity_check`.

## MVP boundary

This release coordinates agents and worktrees within one local Git clone. It
does **not** automatically commit, fetch, replay, or push snapshots between
different clones. Until the remote synchronization protocol is implemented,
teams should serialize snapshot publication and treat a binary merge conflict
as a stop signal. Git hooks can add guardrails, but are not a correctness
boundary. See [ADR 0001](docs/adr/0001-local-first-mvp.md).

## Development

```sh
go build ./...
go test ./...
go test -race ./...
go vet ./...
```
