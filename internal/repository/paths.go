// Package repository discovers issue tracker paths shared by Git worktrees.
package repository

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// Paths contains the live database and versionable export locations.
type Paths struct {
	Root         string
	Database     string
	Snapshot     string
	Manifest     string
	GitCommonDir string
}

// Discover uses Git's common directory for the live database so every worktree
// in one clone coordinates through one SQLite file.
func Discover(ctx context.Context, cwd string) (Paths, error) {
	if cwd == "" {
		return Paths{}, fmt.Errorf("working directory is required")
	}
	inside, err := gitOutput(ctx, cwd, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		if isNotRepository(err) {
			return standalonePaths(cwd)
		}
		return Paths{}, fmt.Errorf("detect git worktree: %w", err)
	}
	if inside == "false" {
		return standalonePaths(cwd)
	}
	if inside != "true" {
		return Paths{}, fmt.Errorf("unexpected git worktree response %q", inside)
	}
	root, err := gitOutput(ctx, cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return Paths{}, fmt.Errorf("find git worktree root: %w", err)
	}
	common, err := gitOutput(ctx, cwd, "rev-parse", "--git-common-dir")
	if err != nil {
		return Paths{}, fmt.Errorf("find git common directory: %w", err)
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(cwd, common)
	}
	common, err = filepath.Abs(common)
	if err != nil {
		return Paths{}, fmt.Errorf("resolve git common directory: %w", err)
	}
	if canonical, err := filepath.EvalSymlinks(common); err == nil {
		common = canonical
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return Paths{}, fmt.Errorf("resolve repository root: %w", err)
	}
	if canonical, err := filepath.EvalSymlinks(root); err == nil {
		root = canonical
	}
	return Paths{
		Root:         root,
		Database:     filepath.Join(common, "issue-tracker", "live.sqlite"),
		Snapshot:     filepath.Join(root, ".issues", "issues.sqlite"),
		Manifest:     filepath.Join(root, ".issues", "manifest.json"),
		GitCommonDir: common,
	}, nil
}

func standalonePaths(cwd string) (Paths, error) {
	absolute, err := filepath.Abs(cwd)
	if err != nil {
		return Paths{}, fmt.Errorf("resolve working directory: %w", err)
	}
	return Paths{
		Root:     absolute,
		Database: filepath.Join(absolute, ".issues", "live.sqlite"),
		Snapshot: filepath.Join(absolute, ".issues", "issues.sqlite"),
		Manifest: filepath.Join(absolute, ".issues", "manifest.json"),
	}, nil
}

type gitCommandError struct {
	args   []string
	output string
	err    error
}

func (e *gitCommandError) Error() string {
	message := fmt.Sprintf("git %s: %v", strings.Join(e.args, " "), e.err)
	if e.output != "" {
		message += ": " + e.output
	}
	return message
}

func (e *gitCommandError) Unwrap() error { return e.err }

func isNotRepository(err error) bool {
	commandErr, ok := err.(*gitCommandError)
	return ok && strings.Contains(strings.ToLower(commandErr.output), "not a git repository")
}

func gitOutput(ctx context.Context, cwd string, args ...string) (string, error) {
	commandArgs := append([]string{"-C", cwd}, args...)
	output, err := exec.CommandContext(ctx, "git", commandArgs...).CombinedOutput()
	if err != nil {
		return "", &gitCommandError{args: commandArgs, output: strings.TrimSpace(string(output)), err: err}
	}
	return strings.TrimSpace(string(output)), nil
}
