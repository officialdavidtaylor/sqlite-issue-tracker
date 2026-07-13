package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func runCommand(t *testing.T, args ...string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), args, &stdout, &stderr); err != nil {
		t.Fatalf("Run(%v) error = %v, stderr = %s", args, err, stderr.String())
	}
	return stdout.String()
}

func TestRun_EndToEnd(t *testing.T) {
	directory := t.TempDir()
	database := filepath.Join(directory, "live.sqlite")
	global := []string{"--db", database}
	runCommand(t, append(global, "init")...)
	created := runCommand(t, append(global, "create", "--id", "ISS-1", "--title", "CLI issue", "--mutation-id", "create-1", "--json")...)
	if !strings.Contains(created, `"revision": 1`) {
		t.Fatalf("create output = %s, want revision 1", created)
	}
	updated := runCommand(t, append(global, "update", "ISS-1", "--status", "closed", "--expected-revision", "1", "--mutation-id", "update-1", "--json")...)
	if !strings.Contains(updated, `"status": "closed"`) || !strings.Contains(updated, `"revision": 2`) {
		t.Fatalf("update output = %s, want closed at revision 2", updated)
	}
	history := runCommand(t, append(global, "history", "ISS-1", "--json")...)
	if !strings.Contains(history, `"operation": "create"`) || !strings.Contains(history, `"operation": "update"`) {
		t.Fatalf("history output = %s, want create and update", history)
	}
	snapshot := filepath.Join(directory, "snapshot.sqlite")
	manifest := filepath.Join(directory, "snapshot.json")
	runCommand(t, append(global, "snapshot", "--output", snapshot, "--manifest", manifest)...)
	runCommand(t, append(global, "verify")...)
}
