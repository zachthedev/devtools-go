// Package scope provides helpers for narrowing a devtool's analysis to a
// subset of packages in a module. Tools accept go-list patterns
// (e.g. ./internal/foo/...) and use [PackageDirs] to resolve them to
// directories, [Matcher] to test whether an allow-list entry or file path
// falls within the scope, and [Filter] to drop out-of-scope items from
// result sets.
//
// Scope is independent of any specific file format or tool: it operates on
// package patterns and file paths. Pair it with format-specific helpers
// (e.g. package allowlist) when building a scoped check.
package scope

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// ///////////////////////////////////////////////
// Package resolution
// ///////////////////////////////////////////////

// PackageDirs resolves go-list package patterns into unique, sorted,
// repo-root-relative directories with forward slashes. Exits the process
// on go-list failure. An empty slice of patterns defaults to ./....
func PackageDirs(patterns []string) []string {
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}
	args := slices.Concat([]string{"list", "-f", "{{.Dir}}"}, patterns)
	cmd := exec.Command("go", args...) //nolint:gosec // args are go list patterns, not user input
	cmd.Stderr = os.Stderr
	output, err := cmd.Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: go list failed: %v\n", err)
		os.Exit(2)
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: getting working directory: %v\n", err)
		os.Exit(2)
	}

	seen := map[string]struct{}{}
	var dirs []string
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		rel, err := filepath.Rel(cwd, line)
		if err != nil {
			continue
		}
		rel = filepath.ToSlash(rel)
		if _, ok := seen[rel]; !ok {
			seen[rel] = struct{}{}
			dirs = append(dirs, rel)
		}
	}
	slices.Sort(dirs)
	return dirs
}

// ///////////////////////////////////////////////
// Matching and filtering
// ///////////////////////////////////////////////

// Matcher returns a predicate reporting whether an allowlist-style entry
// (either a full "kind file message" line or just a file path) falls
// within any of the given package directories. An empty list or one
// containing the repo root matches everything, so unscoped invocations
// keep their existing behavior.
func Matcher(pkgDirs []string) func(entry string) bool {
	if len(pkgDirs) == 0 {
		return func(string) bool { return true }
	}
	for _, d := range pkgDirs {
		if d == "." || d == "" {
			return func(string) bool { return true }
		}
	}
	normalized := make([]string, 0, len(pkgDirs))
	for _, d := range pkgDirs {
		normalized = append(normalized, filepath.ToSlash(d))
	}
	return func(entry string) bool {
		file := entry
		if parts := strings.SplitN(entry, " ", 3); len(parts) >= 2 {
			file = parts[1]
		}
		file = filepath.ToSlash(file)
		for _, d := range normalized {
			if file == d || strings.HasPrefix(file, d+"/") {
				return true
			}
		}
		return false
	}
}

// Filter returns items whose String() form the keep predicate accepts.
// A nil predicate returns the input slice unchanged.
func Filter[T fmt.Stringer](items []T, keep func(string) bool) []T {
	if keep == nil {
		return items
	}
	filtered := make([]T, 0, len(items))
	for _, item := range items {
		if keep(item.String()) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}
