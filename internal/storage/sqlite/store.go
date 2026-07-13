// Package sqlite implements durable issue persistence in SQLite.
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

const timeFormat = time.RFC3339Nano

// Store owns the serialized SQLite connection used for all writes.
type Store struct {
	db            *sql.DB
	path          string
	now           func() time.Time
	hashTableHook func(string)
}

// Open creates or opens a store, configures SQLite, and applies migrations.
func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("database path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	canonical, err := canonicalPath(path)
	if err != nil {
		return nil, fmt.Errorf("resolve database path: %w", err)
	}
	db, err := sql.Open("sqlite", canonical)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	store := &Store{db: db, path: canonical, now: func() time.Time { return time.Now().UTC() }}
	if err := store.configure(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err := store.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

// Close releases the database connection.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) configure(ctx context.Context) error {
	pragmas := []string{
		"PRAGMA busy_timeout = 5000",
		"PRAGMA foreign_keys = ON",
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
	}
	for _, statement := range pragmas {
		if err := s.execPragma(ctx, statement); err != nil {
			return fmt.Errorf("configure sqlite with %q: %w", statement, err)
		}
	}
	return nil
}

func (s *Store) execPragma(ctx context.Context, statement string) error {
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	retry := time.NewTicker(10 * time.Millisecond)
	defer retry.Stop()
	for {
		if _, err := s.db.ExecContext(ctx, statement); err == nil {
			return nil
		} else if !strings.Contains(err.Error(), "SQLITE_BUSY") && !strings.Contains(err.Error(), "database is locked") {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("database remained locked for 5 seconds")
		case <-retry.C:
		}
	}
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
version TEXT PRIMARY KEY,
applied_at TEXT NOT NULL
) STRICT`); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		body, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		if err := s.withImmediate(ctx, func(q querier) error {
			var exists int
			err := q.QueryRowContext(ctx, "SELECT 1 FROM schema_migrations WHERE version = ?", entry.Name()).Scan(&exists)
			if err == nil {
				return nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("check migration %s: %w", entry.Name(), err)
			}
			if _, err := q.ExecContext(ctx, string(body)); err != nil {
				return fmt.Errorf("apply migration %s: %w", entry.Name(), err)
			}
			if _, err := q.ExecContext(ctx, "INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)", entry.Name(), s.now().Format(timeFormat)); err != nil {
				return fmt.Errorf("record migration %s: %w", entry.Name(), err)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

type immediateConn struct {
	conn *sql.Conn
}

func (c immediateConn) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return c.conn.ExecContext(ctx, query, args...)
}

func (c immediateConn) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return c.conn.QueryContext(ctx, query, args...)
}

func (c immediateConn) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return c.conn.QueryRowContext(ctx, query, args...)
}

type querier interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Store) withImmediate(ctx context.Context, fn func(querier) error) (err error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire sqlite connection: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("begin immediate transaction: %w", err)
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), "ROLLBACK") }()
	if err = fn(immediateConn{conn: conn}); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}
