package sqlite

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// canonicalPath resolves symlinks in the longest existing prefix while
// preserving any not-yet-created suffix.
func canonicalPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve absolute path %q: %w", path, err)
	}
	current := filepath.Clean(absolute)
	var suffix []string
	for {
		if _, err := os.Lstat(current); err == nil {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", fmt.Errorf("resolve symlinks in %q: %w", path, err)
			}
			for index := len(suffix) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, suffix[index])
			}
			return filepath.Clean(resolved), nil
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("inspect path %q: %w", path, err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return filepath.Clean(absolute), nil
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

func pathsEquivalent(first, second string) (bool, error) {
	if first == second || strings.EqualFold(first, second) {
		return true, nil
	}
	firstInfo, firstErr := os.Stat(first)
	if firstErr != nil && !os.IsNotExist(firstErr) {
		return false, fmt.Errorf("inspect path %q: %w", first, firstErr)
	}
	secondInfo, secondErr := os.Stat(second)
	if secondErr != nil && !os.IsNotExist(secondErr) {
		return false, fmt.Errorf("inspect path %q: %w", second, secondErr)
	}
	return firstErr == nil && secondErr == nil && os.SameFile(firstInfo, secondInfo), nil
}

func validateRegularTarget(path string) error {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect destination %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("destination %q is not a regular file", path)
	}
	return nil
}

func validateArtifactPaths(livePath, outputPath, manifestPath string) (string, string, error) {
	output, err := canonicalPath(outputPath)
	if err != nil {
		return "", "", err
	}
	manifest, err := canonicalPath(manifestPath)
	if err != nil {
		return "", "", err
	}
	for _, pair := range []struct {
		firstName, first, secondName, second string
	}{
		{"snapshot", output, "manifest", manifest},
		{"snapshot", output, "live database", livePath},
		{"manifest", manifest, "live database", livePath},
	} {
		equivalent, err := pathsEquivalent(pair.first, pair.second)
		if err != nil {
			return "", "", err
		}
		if equivalent {
			return "", "", fmt.Errorf("%s path aliases %s path: %s", pair.firstName, pair.secondName, pair.first)
		}
	}
	if err := validateRegularTarget(output); err != nil {
		return "", "", err
	}
	if err := validateRegularTarget(manifest); err != nil {
		return "", "", err
	}
	return output, manifest, nil
}
