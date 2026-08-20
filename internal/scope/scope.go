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
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// ///////////////////////////////////////////////
// Constants
// ///////////////////////////////////////////////

// listTimeout bounds one go list call. A gate that stalls forever is
// indistinguishable from one still working, and only CI has a clock of
// its own to stop it.
const listTimeout = 2 * time.Minute

// ///////////////////////////////////////////////
// Package resolution
// ///////////////////////////////////////////////

// PackageDirs resolves go-list package patterns into unique, sorted,
// repo-root-relative directories with forward slashes. An empty slice of
// patterns defaults to ./....
//
// Every pattern must resolve to at least one package. go list reports a
// pattern that matches nothing as a warning and still exits zero, so a
// caller reading only the combined output would narrow its scope to
// whatever did match. A scope narrowed to nothing finds nothing, which a
// check reports as a pass.
func PackageDirs(patterns []string) ([]string, error) {
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}

	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("getting working directory: %w", err)
	}

	seen := map[string]struct{}{}
	var dirs []string
	for _, pattern := range patterns {
		matched, err := listDirs(pattern)
		if err != nil {
			return nil, err
		}
		if len(matched) == 0 {
			return nil, fmt.Errorf("pattern %s matched no packages", pattern)
		}
		for _, dir := range matched {
			// Rel fails when the two paths sit on different Windows
			// volumes, which a module reached through a replace directive
			// or a go.work file does. Dropping the directory would shrink
			// the scope without saying so.
			rel, err := filepath.Rel(cwd, dir)
			if err != nil {
				return nil, fmt.Errorf("locating %s relative to %s: %w", dir, cwd, err)
			}
			rel = filepath.ToSlash(rel)
			if _, ok := seen[rel]; !ok {
				seen[rel] = struct{}{}
				dirs = append(dirs, rel)
			}
		}
	}
	slices.Sort(dirs)
	return dirs, nil
}

// listDirs reports the directories of the packages one pattern names.
//
// Patterns are resolved one at a time because go list answers for a whole
// invocation at once, and its warning about a pattern that matched nothing
// reaches only stderr. One pattern per run makes an empty answer
// attributable to the pattern that produced it.
func listDirs(pattern string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), listTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", "list", "-f", "{{.Dir}}", pattern) //nolint:gosec // pattern is a go list pattern
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("go list %s did not finish within %s", pattern, listTimeout)
		}
		if s := strings.TrimSpace(stderr.String()); s != "" {
			return nil, fmt.Errorf("go list %s: %w\n%s", pattern, err, s)
		}
		return nil, fmt.Errorf("go list %s: %w", pattern, err)
	}

	var dirs []string
	for line := range strings.SplitSeq(strings.TrimSpace(stdout.String()), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			dirs = append(dirs, line)
		}
	}
	return dirs, nil
}

// ///////////////////////////////////////////////
// Matching and filtering
// ///////////////////////////////////////////////

// Matcher returns a predicate reporting whether a repo-relative file path
// falls within any of the given package directories. A list containing the
// repo root matches everything, so an unscoped invocation keeps its
// existing behavior.
//
// An empty list matches nothing. No scope means no package was resolved,
// and answering yes there would hand every caller a scope it never
// established.
//
// It takes a path rather than a whole allow-file line, because which field
// of a line holds the path is the tool's business and not this package's:
// testpair writes "kind path detail" and deadcode writes "path func". A
// predicate that guessed would answer no for one of them, and a wrong no
// reads as an entry out of scope rather than as a line it failed to parse.
func Matcher(pkgDirs []string) func(path string) bool {
	if len(pkgDirs) == 0 {
		return func(string) bool { return false }
	}
	for _, d := range pkgDirs {
		if d == "." || d == "" {
			return func(string) bool { return true }
		}
	}
	normalized := make([]string, 0, len(pkgDirs))
	for _, d := range pkgDirs {
		normalized = append(normalized, slashed(d))
	}
	return func(path string) bool {
		path = slashed(path)
		for _, d := range normalized {
			if path == d || strings.HasPrefix(path, d+"/") {
				return true
			}
		}
		return false
	}
}

// slashed rewrites a path into the forward-slash form allow files hold.
//
// filepath.ToSlash leaves a backslash alone everywhere except Windows,
// because a backslash is a legal character in a POSIX filename. An allow
// file is committed and read on every platform, so a path written on
// Windows has to resolve the same way on a Linux runner as it does on the
// machine that produced it.
func slashed(path string) string {
	return strings.ReplaceAll(path, `\`, "/")
}

// Filter returns items whose path the keep predicate accepts. path reports
// where an item's file path is; keep decides whether it is in scope. A nil
// predicate returns the input slice unchanged.
func Filter[T any](items []T, path func(T) string, keep func(string) bool) []T {
	if keep == nil {
		return items
	}
	filtered := make([]T, 0, len(items))
	for _, item := range items {
		if keep(path(item)) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}
