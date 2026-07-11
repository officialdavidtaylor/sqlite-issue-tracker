package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Hashes identifies logical issue state separately from immutable audit history.
type Hashes struct {
	StateSHA256   string `json:"state_sha256"`
	HistorySHA256 string `json:"history_sha256"`
}

// Manifest describes a clean, versionable SQLite snapshot.
type Manifest struct {
	SchemaVersion int    `json:"schema_version"`
	Generation    int64  `json:"generation"`
	FileSHA256    string `json:"file_sha256"`
	StateSHA256   string `json:"state_sha256"`
	HistorySHA256 string `json:"history_sha256"`
	CreatedAt     string `json:"created_at"`
}

type canonicalWriter struct{ hash.Hash }

func (w canonicalWriter) field(value string) {
	_, _ = io.WriteString(w, strconv.Itoa(len(value)))
	_, _ = io.WriteString(w, ":")
	_, _ = io.WriteString(w, value)
	_, _ = io.WriteString(w, ";")
}

func hashQuery(ctx context.Context, q querier, h hash.Hash, table, query string, columns int) error {
	writer := canonicalWriter{h}
	writer.field(table)
	rows, err := q.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("hash %s: %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		values := make([]sql.NullString, columns)
		dest := make([]any, columns)
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return fmt.Errorf("scan %s for hashing: %w", table, err)
		}
		writer.field("row")
		for _, value := range values {
			if value.Valid {
				writer.field("v" + value.String)
			} else {
				writer.field("null")
			}
		}
	}
	return rows.Err()
}

func computeHashes(ctx context.Context, q querier) (Hashes, error) {
	state := sha256.New()
	if err := hashQuery(ctx, q, state, "schema_migrations", `SELECT version FROM schema_migrations ORDER BY version`, 1); err != nil {
		return Hashes{}, err
	}
	if err := hashQuery(ctx, q, state, "issues", `SELECT id, title, body, status, revision, created_at, updated_at, deleted_at
FROM issues ORDER BY id`, 8); err != nil {
		return Hashes{}, err
	}
	if err := hashQuery(ctx, q, state, "issue_links", `SELECT source_issue_id, target_issue_id, relationship, created_at
FROM issue_links ORDER BY source_issue_id, target_issue_id, relationship`, 4); err != nil {
		return Hashes{}, err
	}

	history := sha256.New()
	if err := hashQuery(ctx, q, history, "audit_events", `SELECT event_id, mutation_id, request_hash, issue_id, actor, operation,
expected_revision, resulting_revision, payload, occurred_at FROM audit_events ORDER BY event_id`, 10); err != nil {
		return Hashes{}, err
	}
	return Hashes{
		StateSHA256:   hex.EncodeToString(state.Sum(nil)),
		HistorySHA256: hex.EncodeToString(history.Sum(nil)),
	}, nil
}

// Hashes returns deterministic hashes over canonical logical rows.
func (s *Store) Hashes(ctx context.Context) (Hashes, error) {
	return computeHashes(ctx, s.db)
}

// IntegrityCheck runs SQLite's full database integrity check.
func (s *Store) IntegrityCheck(ctx context.Context) error {
	return integrityCheck(ctx, s.db)
}

func integrityCheck(ctx context.Context, q querier) error {
	var result string
	if err := q.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		return fmt.Errorf("run integrity check: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("sqlite integrity check failed: %s", result)
	}
	rows, err := q.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("run foreign key check: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		var table, parent string
		var rowID, constraint int64
		if err := rows.Scan(&table, &rowID, &parent, &constraint); err != nil {
			return fmt.Errorf("read foreign key violation: %w", err)
		}
		return fmt.Errorf("foreign key violation in %s row %d referencing %s constraint %d", table, rowID, parent, constraint)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate foreign key check: %w", err)
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open snapshot for hashing: %w", err)
	}
	defer file.Close()
	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", fmt.Errorf("hash snapshot: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ExportSnapshot creates a consistent compact database with VACUUM INTO, then
// derives every manifest value from that immutable snapshot.
func (s *Store) ExportSnapshot(ctx context.Context, outputPath, manifestPath string) (Manifest, error) {
	if outputPath == "" || manifestPath == "" {
		return Manifest{}, fmt.Errorf("snapshot and manifest paths are required")
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return Manifest{}, fmt.Errorf("create snapshot directory: %w", err)
	}
	placeholder, err := os.CreateTemp(filepath.Dir(outputPath), ".snapshot-*.sqlite")
	if err != nil {
		return Manifest{}, fmt.Errorf("reserve snapshot path: %w", err)
	}
	temporaryPath := placeholder.Name()
	if err := placeholder.Close(); err != nil {
		return Manifest{}, fmt.Errorf("close snapshot placeholder: %w", err)
	}
	if err := os.Remove(temporaryPath); err != nil {
		return Manifest{}, fmt.Errorf("prepare snapshot path: %w", err)
	}
	defer os.Remove(temporaryPath)
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", temporaryPath); err != nil {
		return Manifest{}, fmt.Errorf("export sqlite snapshot: %w", err)
	}

	snapshotDB, err := sql.Open("sqlite", temporaryPath)
	if err != nil {
		return Manifest{}, fmt.Errorf("open exported snapshot: %w", err)
	}
	snapshotDB.SetMaxOpenConns(1)
	defer snapshotDB.Close()
	if _, err := snapshotDB.ExecContext(ctx, "PRAGMA query_only = ON"); err != nil {
		return Manifest{}, fmt.Errorf("make snapshot read-only: %w", err)
	}
	if err := integrityCheck(ctx, snapshotDB); err != nil {
		return Manifest{}, fmt.Errorf("verify exported snapshot: %w", err)
	}
	hashes, err := computeHashes(ctx, snapshotDB)
	if err != nil {
		return Manifest{}, err
	}
	var generation int64
	if err := snapshotDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_events").Scan(&generation); err != nil {
		return Manifest{}, fmt.Errorf("read snapshot generation: %w", err)
	}
	var schemaVersion int
	if err := snapshotDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&schemaVersion); err != nil {
		return Manifest{}, fmt.Errorf("read schema version: %w", err)
	}
	if err := snapshotDB.Close(); err != nil {
		return Manifest{}, fmt.Errorf("close exported snapshot: %w", err)
	}
	fileHash, err := fileSHA256(temporaryPath)
	if err != nil {
		return Manifest{}, err
	}
	manifest := Manifest{
		SchemaVersion: schemaVersion,
		Generation:    generation,
		FileSHA256:    fileHash,
		StateSHA256:   hashes.StateSHA256,
		HistorySHA256: hashes.HistorySHA256,
		CreatedAt:     s.now().Format(time.RFC3339Nano),
	}
	if err := os.Rename(temporaryPath, outputPath); err != nil {
		return Manifest{}, fmt.Errorf("install snapshot: %w", err)
	}
	if err := writeManifest(manifestPath, manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func writeManifest(path string, manifest Manifest) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create manifest directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".manifest-*.json")
	if err != nil {
		return fmt.Errorf("create manifest: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(manifest); err != nil {
		temporary.Close()
		return fmt.Errorf("encode manifest: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync manifest: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close manifest: %w", err)
	}
	if err := os.Chmod(temporaryPath, 0o644); err != nil {
		return fmt.Errorf("set manifest permissions: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("install manifest: %w", err)
	}
	return nil
}
