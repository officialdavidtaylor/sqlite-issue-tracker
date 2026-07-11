package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/officialdavidtaylor/sqlite-issue-tracker/internal/issue"
)

// AddLink inserts a directed relationship. The special blocks relationship is
// kept acyclic so dependency traversal always forms a DAG.
func (s *Store) AddLink(ctx context.Context, params issue.LinkParams) error {
	params.SourceID = strings.TrimSpace(params.SourceID)
	params.TargetID = strings.TrimSpace(params.TargetID)
	params.Relationship = strings.TrimSpace(params.Relationship)
	params.Actor = normalizeActor(params.Actor)
	if err := validateMutationID(params.MutationID); err != nil {
		return err
	}
	if params.SourceID == params.TargetID {
		return errors.New("an issue cannot link to itself")
	}
	if !issue.ValidateRelationship(params.Relationship) {
		return fmt.Errorf("invalid relationship %q", params.Relationship)
	}
	hash, payload, err := requestHash(struct {
		SourceID, TargetID, Relationship, Actor string
	}{params.SourceID, params.TargetID, params.Relationship, params.Actor})
	if err != nil {
		return err
	}
	return s.withImmediate(ctx, func(q querier) error {
		replayed, err := checkReplay(ctx, q, params.MutationID, hash)
		if err != nil || replayed {
			return err
		}
		if _, err := getIssue(ctx, q, params.SourceID, false); err != nil {
			return fmt.Errorf("source: %w", err)
		}
		if _, err := getIssue(ctx, q, params.TargetID, false); err != nil {
			return fmt.Errorf("target: %w", err)
		}
		if params.Relationship == "blocks" {
			var found int
			err := q.QueryRowContext(ctx, `WITH RECURSIVE reachable(id) AS (
    SELECT target_issue_id FROM issue_links
    WHERE source_issue_id = ? AND relationship = 'blocks'
    UNION
    SELECT links.target_issue_id FROM issue_links links
    JOIN reachable ON links.source_issue_id = reachable.id
    WHERE links.relationship = 'blocks'
)
SELECT 1 FROM reachable WHERE id = ? LIMIT 1`, params.TargetID, params.SourceID).Scan(&found)
			if err == nil {
				return issue.ErrDependencyCycle
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("check dependency cycle: %w", err)
			}
		}
		now := s.now().Format(timeFormat)
		if _, err := q.ExecContext(ctx, `INSERT INTO issue_links
(source_issue_id, target_issue_id, relationship, created_at) VALUES (?, ?, ?, ?)`, params.SourceID, params.TargetID, params.Relationship, now); err != nil {
			return fmt.Errorf("add link: %w", err)
		}
		if _, err := q.ExecContext(ctx, `INSERT INTO audit_events
(mutation_id, request_hash, issue_id, actor, operation, payload, occurred_at)
VALUES (?, ?, ?, ?, 'link', ?, ?)`, params.MutationID, hash, params.SourceID, params.Actor, payload, now); err != nil {
			return fmt.Errorf("record link event: %w", err)
		}
		return nil
	})
}

// RemoveLink removes one exact relationship idempotently at the mutation layer.
func (s *Store) RemoveLink(ctx context.Context, params issue.LinkParams) error {
	params.SourceID = strings.TrimSpace(params.SourceID)
	params.TargetID = strings.TrimSpace(params.TargetID)
	params.Relationship = strings.TrimSpace(params.Relationship)
	params.Actor = normalizeActor(params.Actor)
	if err := validateMutationID(params.MutationID); err != nil {
		return err
	}
	if !issue.ValidateRelationship(params.Relationship) {
		return fmt.Errorf("invalid relationship %q", params.Relationship)
	}
	hash, payload, err := requestHash(struct {
		SourceID, TargetID, Relationship, Actor string
	}{params.SourceID, params.TargetID, params.Relationship, params.Actor})
	if err != nil {
		return err
	}
	return s.withImmediate(ctx, func(q querier) error {
		replayed, err := checkReplay(ctx, q, params.MutationID, hash)
		if err != nil || replayed {
			return err
		}
		deleted, err := q.ExecContext(ctx, `DELETE FROM issue_links
WHERE source_issue_id = ? AND target_issue_id = ? AND relationship = ?`, params.SourceID, params.TargetID, params.Relationship)
		if err != nil {
			return fmt.Errorf("remove link: %w", err)
		}
		if count, _ := deleted.RowsAffected(); count != 1 {
			return errors.New("link not found")
		}
		now := s.now().Format(timeFormat)
		if _, err := q.ExecContext(ctx, `INSERT INTO audit_events
(mutation_id, request_hash, issue_id, actor, operation, payload, occurred_at)
VALUES (?, ?, ?, ?, 'unlink', ?, ?)`, params.MutationID, hash, params.SourceID, params.Actor, payload, now); err != nil {
			return fmt.Errorf("record unlink event: %w", err)
		}
		return nil
	})
}

// ListLinks returns links touching issueID. Direction is incoming, outgoing, or both.
func (s *Store) ListLinks(ctx context.Context, issueID, direction string) ([]issue.Link, error) {
	query := `SELECT source_issue_id, target_issue_id, relationship, created_at FROM issue_links WHERE `
	var args []any
	switch direction {
	case "incoming":
		query += "target_issue_id = ?"
		args = append(args, issueID)
	case "outgoing":
		query += "source_issue_id = ?"
		args = append(args, issueID)
	case "both", "":
		query += "source_issue_id = ? OR target_issue_id = ?"
		args = append(args, issueID, issueID)
	default:
		return nil, fmt.Errorf("invalid direction %q", direction)
	}
	query += " ORDER BY source_issue_id, target_issue_id, relationship"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list links: %w", err)
	}
	defer rows.Close()
	var result []issue.Link
	for rows.Next() {
		var link issue.Link
		var created string
		if err := rows.Scan(&link.SourceID, &link.TargetID, &link.Relationship, &created); err != nil {
			return nil, fmt.Errorf("scan link: %w", err)
		}
		link.CreatedAt, err = parseTime(created)
		if err != nil {
			return nil, err
		}
		result = append(result, link)
	}
	return result, rows.Err()
}

// History returns audit events in acceptance order, optionally for one issue.
func (s *Store) History(ctx context.Context, issueID string) ([]issue.AuditEvent, error) {
	query := `SELECT event_id, mutation_id, COALESCE(issue_id, ''), actor, operation,
expected_revision, resulting_revision, payload, occurred_at FROM audit_events`
	var args []any
	if issueID != "" {
		query += " WHERE issue_id = ?"
		args = append(args, issueID)
	}
	query += " ORDER BY event_id"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("read history: %w", err)
	}
	defer rows.Close()
	var result []issue.AuditEvent
	for rows.Next() {
		var event issue.AuditEvent
		var expected, resulting sql.NullInt64
		var occurred string
		if err := rows.Scan(&event.EventID, &event.MutationID, &event.IssueID, &event.Actor, &event.Operation, &expected, &resulting, &event.Payload, &occurred); err != nil {
			return nil, fmt.Errorf("scan audit event: %w", err)
		}
		if expected.Valid {
			value := expected.Int64
			event.ExpectedRevision = &value
		}
		if resulting.Valid {
			value := resulting.Int64
			event.ResultingRevision = &value
		}
		event.OccurredAt, err = parseTime(occurred)
		if err != nil {
			return nil, err
		}
		result = append(result, event)
	}
	return result, rows.Err()
}
