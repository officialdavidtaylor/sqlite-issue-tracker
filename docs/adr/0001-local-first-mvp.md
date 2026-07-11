# ADR 0001: Local-first SQLite MVP

- Status: Accepted
- Date: 2026-07-11

## Context

Issue state must remain queryable as it grows, support cross-links and audit
history, and tolerate several agents operating in separate Git worktrees. A
SQLite connection pool cannot coordinate independent database copies, while Git
cannot semantically merge SQLite page files.

## Decision

The MVP separates live state from the versioned artifact:

- The live database resides under Git's common directory at
  `.git/issue-tracker/live.sqlite`. All worktrees in one clone discover the same
  path with `git rev-parse --git-common-dir`.
- Every write uses an immediate transaction and produces an audit event in the
  same transaction.
- Entity revisions provide optimistic concurrency control. Mutation IDs provide
  idempotent replay and reject reuse with different request content.
- `.issues/issues.sqlite` is created only through SQLite `VACUUM INTO`.
- `.issues/manifest.json` records schema generation plus exact-file, logical
  state, and audit-history SHA-256 hashes.
- `blocks` links are a directed acyclic graph; other relationship types may be
  cyclic.

## Consequences

Local agents and worktrees safely coordinate without a daemon or connection
pooler. A snapshot is never copied while its source database is open, and a
logical hash is independent of SQLite page layout.

The MVP does not solve concurrent publication from separate clones. The next
protocol must use a dedicated linear branch, durable mutation envelopes, and a
fetch/replay/push retry loop. A rejected push must replay mutations against the
new remote head and surface revision mismatches as semantic conflicts. Hooks
remain optional validation only.
