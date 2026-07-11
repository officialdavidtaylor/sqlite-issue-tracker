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
	root, rootErr := gitOutput(ctx, cwd, "rev-parse", "--show-toplevel")
	common, commonErr := gitOutput(ctx, cwd, "rev-parse", "--git-common-dir")
	if rootErr != nil || commonErr != nil {
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
	if !filepath.IsAbs(common) {
		common = filepath.Join(cwd, common)
	}
	common, err := filepath.Abs(common)
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

func gitOutput(ctx context.Context, cwd string, args ...string) (string, error) {
	commandArgs := append([]string{"-C", cwd}, args...)
	output, err := exec.CommandContext(ctx, "git", commandArgs...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}
