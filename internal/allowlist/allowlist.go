// Package allowlist provides the allow-file format shared by convention-
// enforcing devtools in this module. Each file is a list of canonical
// entries grouped under `# [category]` comment tags; a blank line resets
// the category context so the next group must carry its own tag.
// Uncategorized entries fail the check, preventing rubber-stamped additions.
package allowlist

import (
	"bufio"
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

// Validate checks that entries in an allow file are covered by a comment
// containing a [category] tag. A blank line resets the category context,
// requiring the next group to have its own tag. Returns the text of
// uncategorized entries.
//
// keep filters which uncategorized entries are reported. Pass nil to
// report all of them. Tools that accept scoped package patterns should
// pair this with a scope predicate so pre-existing uncategorized entries
// in untouched parts of the repository don't block a focused run.
func Validate(path string, keep func(entryText string) bool) ([]string, error) {
	if keep == nil {
		keep = func(string) bool { return true }
	}
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
		if !hasCategoryAbove && keep(trimmed) {
			uncategorized = append(uncategorized, trimmed)
		}
	}
	return uncategorized, nil
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
