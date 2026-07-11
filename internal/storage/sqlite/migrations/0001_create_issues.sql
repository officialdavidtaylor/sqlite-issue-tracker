CREATE TABLE issues (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL CHECK (length(trim(title)) > 0),
    body TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL CHECK (status IN ('open', 'in_progress', 'blocked', 'closed')),
    revision INTEGER NOT NULL CHECK (revision >= 1),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    deleted_at TEXT
) STRICT;

CREATE TABLE issue_links (
    source_issue_id TEXT NOT NULL REFERENCES issues(id),
    target_issue_id TEXT NOT NULL REFERENCES issues(id),
    relationship TEXT NOT NULL CHECK (length(relationship) > 0),
    created_at TEXT NOT NULL,
    PRIMARY KEY (source_issue_id, target_issue_id, relationship),
    CHECK (source_issue_id <> target_issue_id)
) STRICT;

CREATE TABLE audit_events (
    event_id INTEGER PRIMARY KEY AUTOINCREMENT,
    mutation_id TEXT NOT NULL UNIQUE,
    request_hash TEXT NOT NULL,
    issue_id TEXT,
    actor TEXT NOT NULL,
    operation TEXT NOT NULL,
    expected_revision INTEGER,
    resulting_revision INTEGER,
    payload TEXT NOT NULL CHECK (json_valid(payload)),
    occurred_at TEXT NOT NULL
) STRICT;

CREATE INDEX issues_status_idx ON issues(status) WHERE deleted_at IS NULL;
CREATE INDEX links_target_idx ON issue_links(target_issue_id, relationship);
CREATE INDEX audit_issue_idx ON audit_events(issue_id, event_id);
