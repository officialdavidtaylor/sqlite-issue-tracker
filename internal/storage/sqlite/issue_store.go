package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/officialdavidtaylor/sqlite-issue-tracker/internal/issue"
)

func requestHash(value any) (string, string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", "", fmt.Errorf("encode mutation payload: %w", err)
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), string(payload), nil
}

func normalizeActor(actor string) string {
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return "unknown"
	}
	return actor
}

func validateMutationID(id string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("mutation ID is required")
	}
	return nil
}

func checkReplay(ctx context.Context, q querier, mutationID, hash string) (bool, error) {
	var existing string
	err := q.QueryRowContext(ctx, "SELECT request_hash FROM audit_events WHERE mutation_id = ?", mutationID).Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check mutation replay: %w", err)
	}
	if existing != hash {
		return false, issue.ErrMutationCollision
	}
	return true, nil
}

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(timeFormat, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse stored timestamp %q: %w", value, err)
	}
	return parsed, nil
}

type rowScanner interface {
	Scan(...any) error
}

func scanIssue(row rowScanner) (issue.Issue, error) {
	var result issue.Issue
	var created, updated string
	var deleted sql.NullString
	if err := row.Scan(&result.ID, &result.Title, &result.Body, &result.Status, &result.Revision, &created, &updated, &deleted); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return issue.Issue{}, issue.ErrNotFound
		}
		return issue.Issue{}, err
	}
	var err error
	result.CreatedAt, err = parseTime(created)
	if err != nil {
		return issue.Issue{}, err
	}
	result.UpdatedAt, err = parseTime(updated)
	if err != nil {
		return issue.Issue{}, err
	}
	if deleted.Valid {
		value, err := parseTime(deleted.String)
		if err != nil {
			return issue.Issue{}, err
		}
		result.DeletedAt = &value
	}
	return result, nil
}

const selectIssue = `SELECT id, title, body, status, revision, created_at, updated_at, deleted_at FROM issues`

func getIssue(ctx context.Context, q querier, id string, includeDeleted bool) (issue.Issue, error) {
	query := selectIssue + " WHERE id = ?"
	if !includeDeleted {
		query += " AND deleted_at IS NULL"
	}
	result, err := scanIssue(q.QueryRowContext(ctx, query, id))
	if err != nil {
		return issue.Issue{}, fmt.Errorf("get issue %s: %w", id, err)
	}
	return result, nil
}

// CreateIssue creates an issue and its audit event atomically. Reusing the same
// mutation ID with the same request is an idempotent replay.
func (s *Store) CreateIssue(ctx context.Context, params issue.CreateParams) (result issue.Issue, err error) {
	params.ID = strings.TrimSpace(params.ID)
	params.Title = strings.TrimSpace(params.Title)
	params.Status = strings.TrimSpace(params.Status)
	params.Actor = normalizeActor(params.Actor)
	if err := validateMutationID(params.MutationID); err != nil {
		return issue.Issue{}, err
	}
	if params.ID == "" {
		return issue.Issue{}, errors.New("issue ID is required")
	}
	if params.Title == "" {
		return issue.Issue{}, errors.New("title is required")
	}
	if !issue.ValidStatus(params.Status) {
		return issue.Issue{}, fmt.Errorf("invalid status %q", params.Status)
	}
	hash, payload, err := requestHash(struct {
		ID, Title, Body, Status, Actor string
	}{params.ID, params.Title, params.Body, params.Status, params.Actor})
	if err != nil {
		return issue.Issue{}, err
	}
	err = s.withImmediate(ctx, func(q querier) error {
		replayed, err := checkReplay(ctx, q, params.MutationID, hash)
		if err != nil {
			return err
		}
		if replayed {
			result, err = getIssue(ctx, q, params.ID, true)
			return err
		}
		now := s.now().Format(timeFormat)
		if _, err := q.ExecContext(ctx, `INSERT INTO issues
(id, title, body, status, revision, created_at, updated_at)
VALUES (?, ?, ?, ?, 1, ?, ?)`, params.ID, params.Title, params.Body, params.Status, now, now); err != nil {
			return fmt.Errorf("insert issue %s: %w", params.ID, err)
		}
		if _, err := q.ExecContext(ctx, `INSERT INTO audit_events
(mutation_id, request_hash, issue_id, actor, operation, resulting_revision, payload, occurred_at)
VALUES (?, ?, ?, ?, 'create', 1, ?, ?)`, params.MutationID, hash, params.ID, params.Actor, payload, now); err != nil {
			return fmt.Errorf("record create event: %w", err)
		}
		result, err = getIssue(ctx, q, params.ID, true)
		return err
	})
	return result, err
}

// GetIssue returns one non-deleted issue.
func (s *Store) GetIssue(ctx context.Context, id string) (issue.Issue, error) {
	return getIssue(ctx, s.db, id, false)
}

// ListIssues returns issues ordered by identifier.
func (s *Store) ListIssues(ctx context.Context, options issue.ListOptions) ([]issue.Issue, error) {
	query := selectIssue + " WHERE 1 = 1"
	var args []any
	if !options.IncludeDeleted {
		query += " AND deleted_at IS NULL"
	}
	if options.Status != "" {
		if !issue.ValidStatus(options.Status) {
			return nil, fmt.Errorf("invalid status %q", options.Status)
		}
		query += " AND status = ?"
		args = append(args, options.Status)
	}
	query += " ORDER BY id"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list issues: %w", err)
	}
	defer rows.Close()
	var result []issue.Issue
	for rows.Next() {
		item, err := scanIssue(rows)
		if err != nil {
			return nil, fmt.Errorf("scan issue: %w", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate issues: %w", err)
	}
	return result, nil
}

// UpdateIssue changes selected fields when the expected revision still matches.
func (s *Store) UpdateIssue(ctx context.Context, params issue.UpdateParams) (result issue.Issue, err error) {
	params.ID = strings.TrimSpace(params.ID)
	params.Actor = normalizeActor(params.Actor)
	if err := validateMutationID(params.MutationID); err != nil {
		return issue.Issue{}, err
	}
	if params.ExpectedRevision < 1 {
		return issue.Issue{}, errors.New("expected revision must be at least 1")
	}
	if params.Title == nil && params.Body == nil && params.Status == nil {
		return issue.Issue{}, errors.New("at least one field must be updated")
	}
	if params.Title != nil {
		value := strings.TrimSpace(*params.Title)
		if value == "" {
			return issue.Issue{}, errors.New("title cannot be empty")
		}
		params.Title = &value
	}
	if params.Status != nil {
		value := strings.TrimSpace(*params.Status)
		if !issue.ValidStatus(value) {
			return issue.Issue{}, fmt.Errorf("invalid status %q", value)
		}
		params.Status = &value
	}
	hash, payload, err := requestHash(struct {
		ID               string
		Title            *string
		Body             *string
		Status           *string
		ExpectedRevision int64
		Actor            string
	}{params.ID, params.Title, params.Body, params.Status, params.ExpectedRevision, params.Actor})
	if err != nil {
		return issue.Issue{}, err
	}
	err = s.withImmediate(ctx, func(q querier) error {
		replayed, err := checkReplay(ctx, q, params.MutationID, hash)
		if err != nil {
			return err
		}
		if replayed {
			result, err = getIssue(ctx, q, params.ID, true)
			return err
		}
		current, err := getIssue(ctx, q, params.ID, false)
		if err != nil {
			return err
		}
		if current.Revision != params.ExpectedRevision {
			return &issue.RevisionConflict{IssueID: params.ID, Expected: params.ExpectedRevision, Actual: current.Revision}
		}
		if params.Title != nil {
			current.Title = *params.Title
		}
		if params.Body != nil {
			current.Body = *params.Body
		}
		if params.Status != nil {
			current.Status = *params.Status
		}
		current.Revision++
		now := s.now().Format(timeFormat)
		update, err := q.ExecContext(ctx, `UPDATE issues SET title = ?, body = ?, status = ?, revision = ?, updated_at = ?
WHERE id = ? AND revision = ? AND deleted_at IS NULL`, current.Title, current.Body, current.Status, current.Revision, now, params.ID, params.ExpectedRevision)
		if err != nil {
			return fmt.Errorf("update issue %s: %w", params.ID, err)
		}
		if changed, _ := update.RowsAffected(); changed != 1 {
			return &issue.RevisionConflict{IssueID: params.ID, Expected: params.ExpectedRevision, Actual: current.Revision - 1}
		}
		if _, err := q.ExecContext(ctx, `INSERT INTO audit_events
(mutation_id, request_hash, issue_id, actor, operation, expected_revision, resulting_revision, payload, occurred_at)
VALUES (?, ?, ?, ?, 'update', ?, ?, ?, ?)`, params.MutationID, hash, params.ID, params.Actor, params.ExpectedRevision, current.Revision, payload, now); err != nil {
			return fmt.Errorf("record update event: %w", err)
		}
		result, err = getIssue(ctx, q, params.ID, true)
		return err
	})
	return result, err
}

// DeleteIssue soft-deletes an issue and preserves its history.
func (s *Store) DeleteIssue(ctx context.Context, params issue.DeleteParams) (result issue.Issue, err error) {
	params.ID = strings.TrimSpace(params.ID)
	params.Actor = normalizeActor(params.Actor)
	if err := validateMutationID(params.MutationID); err != nil {
		return issue.Issue{}, err
	}
	if params.ExpectedRevision < 1 {
		return issue.Issue{}, errors.New("expected revision must be at least 1")
	}
	hash, payload, err := requestHash(struct {
		ID               string
		ExpectedRevision int64
		Actor            string
	}{params.ID, params.ExpectedRevision, params.Actor})
	if err != nil {
		return issue.Issue{}, err
	}
	err = s.withImmediate(ctx, func(q querier) error {
		replayed, err := checkReplay(ctx, q, params.MutationID, hash)
		if err != nil {
			return err
		}
		if replayed {
			result, err = getIssue(ctx, q, params.ID, true)
			return err
		}
		current, err := getIssue(ctx, q, params.ID, false)
		if err != nil {
			return err
		}
		if current.Revision != params.ExpectedRevision {
			return &issue.RevisionConflict{IssueID: params.ID, Expected: params.ExpectedRevision, Actual: current.Revision}
		}
		nextRevision := current.Revision + 1
		now := s.now().Format(timeFormat)
		if _, err := q.ExecContext(ctx, `UPDATE issues SET revision = ?, updated_at = ?, deleted_at = ?
WHERE id = ? AND revision = ? AND deleted_at IS NULL`, nextRevision, now, now, params.ID, params.ExpectedRevision); err != nil {
			return fmt.Errorf("delete issue %s: %w", params.ID, err)
		}
		if _, err := q.ExecContext(ctx, `INSERT INTO audit_events
(mutation_id, request_hash, issue_id, actor, operation, expected_revision, resulting_revision, payload, occurred_at)
VALUES (?, ?, ?, ?, 'delete', ?, ?, ?, ?)`, params.MutationID, hash, params.ID, params.Actor, params.ExpectedRevision, nextRevision, payload, now); err != nil {
			return fmt.Errorf("record delete event: %w", err)
		}
		result, err = getIssue(ctx, q, params.ID, true)
		return err
	})
	return result, err
}
