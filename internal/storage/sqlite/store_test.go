package sqlite

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/officialdavidtaylor/sqlite-issue-tracker/internal/issue"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "issues.sqlite"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return store
}

func createTestIssue(t *testing.T, store *Store, id, mutation string) issue.Issue {
	t.Helper()
	created, err := store.CreateIssue(context.Background(), issue.CreateParams{
		MutationID: mutation,
		ID:         id,
		Title:      "Issue " + id,
		Status:     "open",
		Actor:      "test",
	})
	if err != nil {
		t.Fatalf("CreateIssue(%s) error = %v", id, err)
	}
	return created
}

func TestStore_MutationReplayAndRevisionConflict(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	params := issue.CreateParams{
		MutationID: "create-1",
		ID:         "ISS-1",
		Title:      "First",
		Status:     "open",
		Actor:      "agent-a",
	}
	created, err := store.CreateIssue(ctx, params)
	if err != nil {
		t.Fatalf("CreateIssue() error = %v", err)
	}
	replayed, err := store.CreateIssue(ctx, params)
	if err != nil {
		t.Fatalf("CreateIssue() replay error = %v", err)
	}
	if replayed.Revision != created.Revision {
		t.Fatalf("replayed revision = %d, want %d", replayed.Revision, created.Revision)
	}

	collision := params
	collision.Title = "Different request"
	if _, err := store.CreateIssue(ctx, collision); !errors.Is(err, issue.ErrMutationCollision) {
		t.Fatalf("CreateIssue() collision error = %v, want ErrMutationCollision", err)
	}

	status := "in_progress"
	update := issue.UpdateParams{
		MutationID:       "update-1",
		ID:               "ISS-1",
		Status:           &status,
		ExpectedRevision: 1,
		Actor:            "agent-a",
	}
	updated, err := store.UpdateIssue(ctx, update)
	if err != nil {
		t.Fatalf("UpdateIssue() error = %v", err)
	}
	if updated.Revision != 2 || updated.Status != status {
		t.Fatalf("UpdateIssue() = %+v, want revision 2 and status %s", updated, status)
	}
	replayedUpdate, err := store.UpdateIssue(ctx, update)
	if err != nil {
		t.Fatalf("UpdateIssue() replay error = %v", err)
	}
	if replayedUpdate.Revision != 2 {
		t.Fatalf("replayed update revision = %d, want 2", replayedUpdate.Revision)
	}

	title := "Stale update"
	_, err = store.UpdateIssue(ctx, issue.UpdateParams{
		MutationID:       "update-stale",
		ID:               "ISS-1",
		Title:            &title,
		ExpectedRevision: 1,
		Actor:            "agent-b",
	})
	var conflict *issue.RevisionConflict
	if !errors.As(err, &conflict) {
		t.Fatalf("UpdateIssue() stale error = %v, want RevisionConflict", err)
	}
	if conflict.Actual != 2 {
		t.Fatalf("conflict actual revision = %d, want 2", conflict.Actual)
	}

	history, err := store.History(ctx, "ISS-1")
	if err != nil {
		t.Fatalf("History() error = %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("history length = %d, want 2", len(history))
	}
}

func TestStore_FailedMutationRollsBack(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	createTestIssue(t, store, "ISS-1", "create-1")
	_, err := store.CreateIssue(ctx, issue.CreateParams{
		MutationID: "create-duplicate",
		ID:         "ISS-1",
		Title:      "Duplicate",
		Status:     "open",
		Actor:      "test",
	})
	if err == nil {
		t.Fatal("CreateIssue() duplicate error = nil")
	}
	history, err := store.History(ctx, "")
	if err != nil {
		t.Fatalf("History() error = %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history length after rollback = %d, want 1", len(history))
	}
}

func TestStore_DeleteIsSoftAndAudited(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	createTestIssue(t, store, "ISS-1", "create-1")
	deleted, err := store.DeleteIssue(ctx, issue.DeleteParams{
		MutationID:       "delete-1",
		ID:               "ISS-1",
		ExpectedRevision: 1,
		Actor:            "test",
	})
	if err != nil {
		t.Fatalf("DeleteIssue() error = %v", err)
	}
	if deleted.DeletedAt == nil || deleted.Revision != 2 {
		t.Fatalf("DeleteIssue() = %+v, want tombstone at revision 2", deleted)
	}
	if _, err := store.GetIssue(ctx, "ISS-1"); !errors.Is(err, issue.ErrNotFound) {
		t.Fatalf("GetIssue() deleted error = %v, want ErrNotFound", err)
	}
	visible, err := store.ListIssues(ctx, issue.ListOptions{})
	if err != nil || len(visible) != 0 {
		t.Fatalf("ListIssues() visible = %v, %v; want empty", visible, err)
	}
	all, err := store.ListIssues(ctx, issue.ListOptions{IncludeDeleted: true})
	if err != nil || len(all) != 1 || all[0].DeletedAt == nil {
		t.Fatalf("ListIssues(all) = %v, %v; want tombstone", all, err)
	}
}

func TestStore_BlocksLinksRejectCycles(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	for index, id := range []string{"A", "B", "C"} {
		createTestIssue(t, store, id, fmt.Sprintf("create-%d", index))
	}
	for index, edge := range [][2]string{{"A", "B"}, {"B", "C"}} {
		if err := store.AddLink(ctx, issue.LinkParams{
			MutationID:   fmt.Sprintf("link-%d", index),
			SourceID:     edge[0],
			TargetID:     edge[1],
			Relationship: "blocks",
			Actor:        "test",
		}); err != nil {
			t.Fatalf("AddLink(%v) error = %v", edge, err)
		}
	}
	cycle := issue.LinkParams{MutationID: "link-cycle", SourceID: "C", TargetID: "A", Relationship: "blocks", Actor: "test"}
	if err := store.AddLink(ctx, cycle); !errors.Is(err, issue.ErrDependencyCycle) {
		t.Fatalf("AddLink(cycle) error = %v, want ErrDependencyCycle", err)
	}
	links, err := store.ListLinks(ctx, "B", "both")
	if err != nil {
		t.Fatalf("ListLinks() error = %v", err)
	}
	if len(links) != 2 {
		t.Fatalf("ListLinks() length = %d, want 2", len(links))
	}

	replay := issue.LinkParams{MutationID: "link-0", SourceID: "A", TargetID: "B", Relationship: "blocks", Actor: "test"}
	if err := store.AddLink(ctx, replay); err != nil {
		t.Fatalf("AddLink() replay error = %v", err)
	}
	if err := store.RemoveLink(ctx, issue.LinkParams{MutationID: "unlink-1", SourceID: "A", TargetID: "B", Relationship: "blocks", Actor: "test"}); err != nil {
		t.Fatalf("RemoveLink() error = %v", err)
	}
	if err := store.RemoveLink(ctx, issue.LinkParams{MutationID: "unlink-1", SourceID: "A", TargetID: "B", Relationship: "blocks", Actor: "test"}); err != nil {
		t.Fatalf("RemoveLink() replay error = %v", err)
	}
}

func TestStore_ConcurrentCreatesAreSerialized(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	const count = 24
	var wait sync.WaitGroup
	errorsByWorker := make(chan error, count)
	for index := 0; index < count; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			_, err := store.CreateIssue(ctx, issue.CreateParams{
				MutationID: fmt.Sprintf("mutation-%d", index),
				ID:         fmt.Sprintf("ISS-%02d", index),
				Title:      fmt.Sprintf("Concurrent %d", index),
				Status:     "open",
				Actor:      "concurrency-test",
			})
			errorsByWorker <- err
		}(index)
	}
	wait.Wait()
	close(errorsByWorker)
	for err := range errorsByWorker {
		if err != nil {
			t.Errorf("CreateIssue() concurrent error = %v", err)
		}
	}
	items, err := store.ListIssues(ctx, issue.ListOptions{})
	if err != nil {
		t.Fatalf("ListIssues() error = %v", err)
	}
	if len(items) != count {
		t.Fatalf("ListIssues() count = %d, want %d", len(items), count)
	}
}

func TestOpen_ConcurrentInitializationIsSerialized(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "shared.sqlite")
	const count = 8
	start := make(chan struct{})
	errorsByWorker := make(chan error, count)
	var wait sync.WaitGroup
	for index := 0; index < count; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			store, err := Open(ctx, path)
			if err == nil {
				err = store.Close()
			}
			errorsByWorker <- err
		}()
	}
	close(start)
	wait.Wait()
	close(errorsByWorker)
	for err := range errorsByWorker {
		if err != nil {
			t.Errorf("Open() concurrent error = %v", err)
		}
	}
}

func TestStore_HashesAndSnapshotDescribeExportedState(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	store.now = func() time.Time { return time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC) }
	createTestIssue(t, store, "ISS-1", "create-1")
	liveHashes, err := store.Hashes(ctx)
	if err != nil {
		t.Fatalf("Hashes() error = %v", err)
	}
	directory := t.TempDir()
	snapshotPath := filepath.Join(directory, "issues.sqlite")
	manifestPath := filepath.Join(directory, "manifest.json")
	manifest, err := store.ExportSnapshot(ctx, snapshotPath, manifestPath)
	if err != nil {
		t.Fatalf("ExportSnapshot() error = %v", err)
	}
	if manifest.StateSHA256 != liveHashes.StateSHA256 || manifest.HistorySHA256 != liveHashes.HistorySHA256 {
		t.Fatalf("manifest hashes = %+v, want %+v", manifest, liveHashes)
	}
	actualFileHash, err := fileSHA256(snapshotPath)
	if err != nil {
		t.Fatalf("fileSHA256() error = %v", err)
	}
	if manifest.FileSHA256 != actualFileHash {
		t.Fatalf("manifest file hash = %s, want %s", manifest.FileSHA256, actualFileHash)
	}
	if _, err := os.Stat(snapshotPath + "-wal"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("snapshot WAL exists or stat failed unexpectedly: %v", err)
	}
	if err := store.IntegrityCheck(ctx); err != nil {
		t.Fatalf("IntegrityCheck() error = %v", err)
	}
}
