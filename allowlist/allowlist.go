// Package allowlist provides shared infrastructure for devtools that enforce
// conventions via allow-list gated checks. Both deadcode and testpair use
// the same file format: comment-separated groups where every entry must be
// covered by a # [category] tag.
package allowlist

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// ///////////////////////////////////////////////
// Types
// ///////////////////////////////////////////////

// Category describes a valid allow-list category tag for the file header.
type Category struct {
	Tag         string // e.g., "public-api"
	Description string // e.g., "exported API not yet called from cmd/"
}

// ///////////////////////////////////////////////
// Validation
// ///////////////////////////////////////////////

// Validate checks that every entry in an allow file is covered by a comment
// containing a [category] tag. A blank line resets the category context,
// requiring the next group to have its own tag. Returns the text of
// uncategorized entries.
func Validate(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading allow file %s: %w", path, err)
	}

	var uncategorized []string
	hasCategoryAbove := false

	for _, raw := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			hasCategoryAbove = false
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			if strings.Contains(trimmed, "[") && strings.Contains(trimmed, "]") {
				hasCategoryAbove = true
			}
			continue
		}
		if !hasCategoryAbove {
			uncategorized = append(uncategorized, trimmed)
		}
	}
	return uncategorized, nil
}

// FailOnUncategorized runs Validate and exits with an error if any entries
// lack a category tag.
func FailOnUncategorized(path string) {
	uncategorized, err := Validate(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
	if len(uncategorized) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "FAIL: %d allow-list entries have no [category] tag:\n", len(uncategorized))
	for _, line := range uncategorized {
		fmt.Fprintf(os.Stderr, "  %s\n", line)
	}
	fmt.Fprintf(os.Stderr, "\nEvery entry needs a # [category] comment above its group.\n")
	os.Exit(1)
}

// ///////////////////////////////////////////////
// I/O
// ///////////////////////////////////////////////

// WriteUpdate writes an allow file with a standard header and uncategorized
// entries. The regenerate command tells users how to refresh. The blank line
// between header and entries resets category context so the check enforces
// that categories are added before committing.
func WriteUpdate(path string, toolName string, regenerateCmd string, categories []Category, entries []string) error {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s allow list.\n", toolName)
	sb.WriteString("# Every entry must have a # [category] comment above its group.\n")
	sb.WriteString("# Entries without a category tag will fail the check.\n")
	sb.WriteString("#\n")
	fmt.Fprintf(&sb, "# Regenerate: %s\n", regenerateCmd)
	sb.WriteString("#\n")
	sb.WriteString("# Categories:\n")
	for _, c := range categories {
		fmt.Fprintf(&sb, "#   [%s]  %s\n", c.Tag, c.Description)
	}
	sb.WriteString("\n") // blank line resets category context
	for _, e := range entries {
		sb.WriteString(e)
		sb.WriteString("\n")
	}
	return os.WriteFile(path, []byte(sb.String()), 0o644) //nolint:gosec // allow lists are not secrets
}

// LoadLines reads an allow file and returns non-comment, non-blank lines.
// Each line is stripped of inline comments and trimmed.
func LoadLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening allow file %s: %w", path, err)
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if idx := strings.Index(line, "#"); idx >= 0 {
			line = line[:idx]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}
	return lines, nil
}

// ///////////////////////////////////////////////
// Output
// ///////////////////////////////////////////////

// WriteJSON encodes a value as indented JSON to stdout. Exits on error.
func WriteJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fmt.Fprintf(os.Stderr, "error: encoding JSON: %v\n", err)
		os.Exit(2)
	}
}

// Coalesce returns an empty slice instead of nil for clean JSON serialization.
func Coalesce[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
