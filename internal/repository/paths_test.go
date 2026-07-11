package repository

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func runGit(t *testing.T, directory string, args ...string) {
	t.Helper()
	commandArgs := append([]string{"-C", directory}, args...)
	command := exec.Command("git", commandArgs...)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v error = %v\n%s", args, err, output)
	}
}

func TestDiscover_SharesDatabaseAcrossWorktrees(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repository := filepath.Join(t.TempDir(), "repository")
	if err := os.MkdirAll(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "init", "-b", "main")
	runGit(t, repository, "config", "user.name", "Test")
	runGit(t, repository, "config", "user.email", "test@example.invalid")
	runGit(t, repository, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "add", "README.md")
	runGit(t, repository, "commit", "-m", "initial")
	worktree := filepath.Join(filepath.Dir(repository), "worktree")
	runGit(t, repository, "worktree", "add", "--detach", worktree)

	primary, err := Discover(context.Background(), repository)
	if err != nil {
		t.Fatalf("Discover(primary) error = %v", err)
	}
	secondary, err := Discover(context.Background(), worktree)
	if err != nil {
		t.Fatalf("Discover(worktree) error = %v", err)
	}
	if primary.Database != secondary.Database {
		t.Fatalf("database paths differ: %q != %q", primary.Database, secondary.Database)
	}
	if primary.Snapshot == secondary.Snapshot {
		t.Fatalf("snapshot paths unexpectedly match: %q", primary.Snapshot)
	}
}

func TestDiscover_UsesStandalonePathsOutsideGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	directory := t.TempDir()
	paths, err := Discover(context.Background(), directory)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if paths.GitCommonDir != "" {
		t.Fatalf("GitCommonDir = %q, want empty", paths.GitCommonDir)
	}
	if paths.Database != filepath.Join(directory, ".issues", "live.sqlite") {
		t.Fatalf("Database = %q, want standalone path", paths.Database)
	}
}

func TestDiscover_PropagatesMissingGitInsideRepository(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	repository := filepath.Join(t.TempDir(), "repository")
	if err := os.MkdirAll(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(gitPath, "-C", repository, "init", "-b", "main")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init error = %v\n%s", err, output)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := Discover(context.Background(), repository); err == nil {
		t.Fatal("Discover() with missing Git error = nil")
	}
	if _, err := os.Stat(filepath.Join(repository, ".issues")); !os.IsNotExist(err) {
		t.Fatalf("standalone directory was created after Git failure: %v", err)
	}
}
