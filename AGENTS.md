# Repository Guidelines

## Project Structure & Module Organization

This Go repository is currently pre-scaffold. Keep entry points in `cmd/<name>/`, private packages in `internal/`, SQLite code and migrations in `internal/storage/sqlite/`, and decisions in `docs/adr/`. Keep tests beside code as `*_test.go` and fixtures in package-local `testdata/`. Reserve `pkg/` for intentionally public APIs. Keep generated databases, `*.sqlite-wal`, and `*.sqlite-shm` out of source directories. Put any versioned snapshot at a stable path such as `.issues/issues.sqlite`.

Separate CLI parsing, domain operations, SQLite persistence, Git synchronization, and hashing. Route database writes through the persistence layer.

## Build, Test, and Development Commands

After the first change initializes `go.mod`, use the standard Go toolchain:

- `go build ./...` — compile every command and package.
- `go test ./...` — run the complete test suite.
- `go test -race ./...` — detect unsafe concurrent access.
- `go vet ./...` — run Go's static checks.
- `gofmt -w .` — format Go source before committing.
- `sqlite3 <database> 'PRAGMA integrity_check;'` — verify a generated snapshot.

## Coding Style & Naming Conventions

Follow Effective Go and let `gofmt` control formatting. Use short, lowercase package names, `MixedCaps` identifiers, and filenames such as `mutation_store.go`. Document exported declarations. Accept `context.Context` first for I/O, wrap errors with `%w`, and avoid package-level mutable state. Use `snake_case` SQL identifiers, `kebab-case` CLI flags, and migration names like `0001_create_issues.sql`. Prefer explicit transactions, foreign keys, parameterized queries, and deterministic serialization.

## Testing Guidelines

Use Go's `testing` package, table-driven cases where useful, and names such as `TestMutationStore_ReplayIsIdempotent`. Persistence changes should cover success, rollback, and constraint failure. Git synchronization tests should cover concurrent writers, rejected pushes, replay, and crash recovery. Use `t.TempDir()` for temporary repositories and databases. Run the race detector for concurrency changes.

## Commit & Pull Request Guidelines

Use concise Conventional Commit subjects, for example `feat(db): add issue audit events`. Keep schema changes and their migrations in the same commit.

Pull requests should explain behavior, migration impact, and verification. Link relevant issues or ADRs. Include terminal output for CLI changes and call out compatibility, locking, or recovery risks.

## Data Safety

Never commit credentials or machine-specific paths. Do not copy a live SQLite file while it is open; use SQLite's backup API or a controlled snapshot operation. Treat Git hooks as guardrails, not as the concurrency or integrity boundary.
