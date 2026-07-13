// Package issue defines the issue tracker's domain model.
package issue

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrNotFound          = errors.New("issue not found")
	ErrMutationCollision = errors.New("mutation ID was already used for a different request")
	ErrDependencyCycle   = errors.New("dependency link would create a cycle")
)

// RevisionConflict reports an optimistic-lock failure.
type RevisionConflict struct {
	IssueID  string
	Expected int64
	Actual   int64
}

func (e *RevisionConflict) Error() string {
	return fmt.Sprintf("issue %s revision conflict: expected %d, current %d", e.IssueID, e.Expected, e.Actual)
}

// Issue is the current materialized state of an issue.
type Issue struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	Body      string     `json:"body"`
	Status    string     `json:"status"`
	Revision  int64      `json:"revision"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
}

// Link is a directed relationship between two issues.
type Link struct {
	SourceID     string    `json:"source_id"`
	TargetID     string    `json:"target_id"`
	Relationship string    `json:"relationship"`
	CreatedAt    time.Time `json:"created_at"`
}

// AuditEvent records one accepted mutation.
type AuditEvent struct {
	EventID           int64     `json:"event_id"`
	MutationID        string    `json:"mutation_id"`
	IssueID           string    `json:"issue_id,omitempty"`
	Actor             string    `json:"actor"`
	Operation         string    `json:"operation"`
	ExpectedRevision  *int64    `json:"expected_revision,omitempty"`
	ResultingRevision *int64    `json:"resulting_revision,omitempty"`
	Payload           string    `json:"payload"`
	OccurredAt        time.Time `json:"occurred_at"`
}

// CreateParams describes an idempotent issue creation.
type CreateParams struct {
	MutationID string
	ID         string
	Title      string
	Body       string
	Status     string
	Actor      string
}

// UpdateParams describes an optimistic, idempotent issue update.
type UpdateParams struct {
	MutationID       string
	ID               string
	Title            *string
	Body             *string
	Status           *string
	ExpectedRevision int64
	Actor            string
}

// DeleteParams describes an optimistic soft deletion.
type DeleteParams struct {
	MutationID       string
	ID               string
	ExpectedRevision int64
	Actor            string
}

// LinkParams describes an idempotent link mutation.
type LinkParams struct {
	MutationID   string
	SourceID     string
	TargetID     string
	Relationship string
	Actor        string
}

// ListOptions filters issue listing.
type ListOptions struct {
	Status         string
	IncludeDeleted bool
}

// ValidStatus reports whether status belongs to the stable MVP state machine.
func ValidStatus(status string) bool {
	switch status {
	case "open", "in_progress", "blocked", "closed":
		return true
	default:
		return false
	}
}

// ValidateRelationship accepts lowercase, underscore-separated relationship names.
func ValidateRelationship(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if (r < 'a' || r > 'z') && r != '_' {
			return false
		}
	}
	return !strings.HasPrefix(value, "_") && !strings.HasSuffix(value, "_")
}
