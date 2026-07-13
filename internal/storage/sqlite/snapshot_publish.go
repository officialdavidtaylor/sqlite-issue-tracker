package sqlite

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const snapshotLockTimeout = 5 * time.Second

func acquireTargetLocks(ctx context.Context, targets ...string) (func(), error) {
	lockPaths := make([]string, 0, len(targets))
	seen := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		lockPath := target + ".sit-lock"
		if _, ok := seen[lockPath]; ok {
			continue
		}
		seen[lockPath] = struct{}{}
		lockPaths = append(lockPaths, lockPath)
	}
	sort.Strings(lockPaths)
	acquired := make([]string, 0, len(lockPaths))
	for _, lockPath := range lockPaths {
		if err := acquireDirectoryLock(ctx, lockPath); err != nil {
			for index := len(acquired) - 1; index >= 0; index-- {
				_ = os.Remove(acquired[index])
			}
			return nil, err
		}
		acquired = append(acquired, lockPath)
	}
	return func() {
		for index := len(acquired) - 1; index >= 0; index-- {
			_ = os.Remove(acquired[index])
		}
	}, nil
}

func acquireDirectoryLock(ctx context.Context, path string) error {
	timeout := time.NewTimer(snapshotLockTimeout)
	defer timeout.Stop()
	retry := time.NewTicker(10 * time.Millisecond)
	defer retry.Stop()
	for {
		if err := os.Mkdir(path, 0o700); err == nil {
			return nil
		} else if !os.IsExist(err) {
			return fmt.Errorf("acquire snapshot lock %q: %w", path, err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("acquire snapshot lock %q: %w", path, ctx.Err())
		case <-timeout.C:
			return fmt.Errorf("snapshot target remains locked at %q; remove this stale lock directory if no exporter is running", path)
		case <-retry.C:
		}
	}
}

type renameFile func(string, string) error

type publicationTarget struct {
	prepared  string
	target    string
	backup    string
	installed bool
}

func publishPair(snapshotTemp, manifestTemp, snapshotPath, manifestPath string, rename renameFile) error {
	targets := []*publicationTarget{
		{prepared: snapshotTemp, target: snapshotPath},
		{prepared: manifestTemp, target: manifestPath},
	}
	for _, target := range targets {
		backup, err := backupExisting(target.target, rename)
		if err != nil {
			rollbackErr := rollbackPublication(targets, rename)
			return errors.Join(fmt.Errorf("back up artifact %q: %w", target.target, err), rollbackErr)
		}
		target.backup = backup
	}
	for _, target := range targets {
		if err := rename(target.prepared, target.target); err != nil {
			rollbackErr := rollbackPublication(targets, rename)
			return errors.Join(fmt.Errorf("install artifact %q: %w", target.target, err), rollbackErr)
		}
		target.installed = true
	}
	for _, directory := range uniqueDirectories(snapshotPath, manifestPath) {
		if err := syncDirectory(directory); err != nil {
			rollbackErr := rollbackPublication(targets, rename)
			return errors.Join(fmt.Errorf("sync artifact directory %q: %w", directory, err), rollbackErr)
		}
	}
	for _, target := range targets {
		if target.backup != "" {
			_ = os.Remove(target.backup)
			target.backup = ""
		}
	}
	return nil
}

func backupExisting(target string, rename renameFile) (string, error) {
	if _, err := os.Stat(target); os.IsNotExist(err) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	placeholder, err := os.CreateTemp(filepath.Dir(target), ".sit-backup-*")
	if err != nil {
		return "", err
	}
	backup := placeholder.Name()
	if err := placeholder.Close(); err != nil {
		return "", err
	}
	if err := os.Remove(backup); err != nil {
		return "", err
	}
	if err := rename(target, backup); err != nil {
		return "", err
	}
	return backup, nil
}

func rollbackPublication(targets []*publicationTarget, rename renameFile) error {
	var rollbackErrors []error
	for index := len(targets) - 1; index >= 0; index-- {
		target := targets[index]
		if target.installed {
			if err := os.Remove(target.target); err != nil && !os.IsNotExist(err) {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("remove partial artifact %q: %w", target.target, err))
			}
			target.installed = false
		}
		if target.backup != "" {
			if err := rename(target.backup, target.target); err != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("restore artifact %q: %w", target.target, err))
			} else {
				target.backup = ""
			}
		}
	}
	return errors.Join(rollbackErrors...)
}

func uniqueDirectories(paths ...string) []string {
	seen := make(map[string]struct{}, len(paths))
	var result []string
	for _, path := range paths {
		directory := filepath.Dir(path)
		if _, ok := seen[directory]; ok {
			continue
		}
		seen[directory] = struct{}{}
		result = append(result, directory)
	}
	sort.Strings(result)
	return result
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
