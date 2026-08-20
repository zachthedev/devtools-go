// Package allowlist provides the allow-file format shared by convention-
// enforcing devtools in this module. Each file is a list of canonical
// entries grouped under `# [category]` comment tags; a blank line resets
// the category context so the next group must carry its own tag.
// Uncategorized entries fail the check, preventing rubber-stamped additions.
package allowlist

import (
	"bufio"
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"slices"
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

// Entry is one allow-file line together with the category governing it.
// An empty Tag marks an entry that carries none, which fails the check.
type Entry struct {
	Tag  string
	Line string
}

// ///////////////////////////////////////////////
// Constants
// ///////////////////////////////////////////////

// categoryTag matches a comment whose entire content is one bracketed tag.
//
// The tag has to stand alone. The header written by [WriteUpdate] lists
// every category with its description on the same line, so a looser match
// would read those lines as tags and let the header authorize the whole
// file. One deleted blank line would then turn the check permissive, in a
// diff that shows nothing else.
var categoryTag = regexp.MustCompile(`^#[ \t]*\[([^\[\]]+)\][ \t]*$`)

// ///////////////////////////////////////////////
// Validation
// ///////////////////////////////////////////////

// Validate checks that entries in an allow file sit under a comment naming
// one of the declared categories. A blank line resets the category
// context, requiring the next group to carry its own tag. It returns the
// text of uncategorized entries.
//
// An unknown tag is an error rather than a missing category, because the
// two need different answers: one is an entry nobody classified, the other
// is a classification this tool does not offer.
//
// keep filters which uncategorized entries are reported. Pass nil to
// report all of them. Tools that accept scoped package patterns should
// pair this with a scope predicate so pre-existing uncategorized entries
// in untouched parts of the repository don't block a focused run.
func Validate(path string, categories []Category, keep func(entryText string) bool) ([]string, error) {
	entries, err := LoadEntries(path, categories)
	if err != nil {
		return nil, err
	}
	if keep == nil {
		keep = func(string) bool { return true }
	}

	var uncategorized []string
	for _, e := range entries {
		if e.Tag == "" && keep(e.Line) {
			uncategorized = append(uncategorized, e.Line)
		}
	}
	return uncategorized, nil
}

// ///////////////////////////////////////////////
// I/O
// ///////////////////////////////////////////////

// LoadEntries reads an allow file into its entries, each carrying the tag
// of the group it sits under. Comments and blank lines are not entries.
func LoadEntries(path string, categories []Category) ([]Entry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading allow file %s: %w", path, err)
	}

	declared := make(map[string]struct{}, len(categories))
	for _, c := range categories {
		declared[c.Tag] = struct{}{}
	}

	var entries []Entry
	tag := ""
	for i, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			tag = ""
			continue
		}
		if strings.HasPrefix(line, "#") {
			m := categoryTag.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			found := strings.TrimSpace(m[1])
			if _, ok := declared[found]; !ok {
				return nil, fmt.Errorf("%s line %d: unknown category [%s]; declared are %s",
					path, i+1, found, declaredList(categories))
			}
			tag = found
			continue
		}
		entries = append(entries, Entry{Tag: tag, Line: line})
	}
	return entries, nil
}

// declaredList renders the declared tags for an error message.
func declaredList(categories []Category) string {
	tags := make([]string, 0, len(categories))
	for _, c := range categories {
		tags = append(tags, "["+c.Tag+"]")
	}
	if len(tags) == 0 {
		return "(none)"
	}
	return strings.Join(tags, " ")
}

// WriteUpdate writes an allow file with a standard header. Entries that
// carry a tag are grouped under it; entries with none follow, where the
// check will report them until somebody classifies them.
//
// The regenerate command tells users how to refresh. The blank line
// between header and entries resets category context, so the header cannot
// stand in for a tag.
func WriteUpdate(path string, toolName string, regenerateCmd string, categories []Category, entries []Entry) error {
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

	// Tagged groups in the order the categories are declared, so a
	// regenerated file reads the same way twice.
	for _, c := range categories {
		lines := linesWithTag(entries, c.Tag)
		if len(lines) == 0 {
			continue
		}
		fmt.Fprintf(&sb, "\n# [%s]\n", c.Tag)
		for _, l := range lines {
			sb.WriteString(l)
			sb.WriteString("\n")
		}
	}

	if untagged := linesWithTag(entries, ""); len(untagged) > 0 {
		sb.WriteString("\n") // blank line resets category context
		for _, l := range untagged {
			sb.WriteString(l)
			sb.WriteString("\n")
		}
	}

	return os.WriteFile(path, []byte(sb.String()), 0o644) //nolint:gosec // allow lists are not secrets
}

// linesWithTag reports the sorted lines of every entry carrying one tag.
func linesWithTag(entries []Entry, tag string) []string {
	var lines []string
	for _, e := range entries {
		if e.Tag == tag {
			lines = append(lines, e.Line)
		}
	}
	slices.SortFunc(lines, cmp.Compare)
	return lines
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
		line, _, _ := strings.Cut(scanner.Text(), "#")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}
	// A scan that stops early answers with the lines it reached and no
	// error of its own. Leaving that unchecked turns an I/O failure into a
	// shorter allow list, and every entry past the stopping point then
	// reads as a new finding.
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading allow file %s: %w", path, err)
	}
	return lines, nil
}

// IsNotExist reports whether err is a missing allow file.
//
// The errors here are wrapped, and os.IsNotExist does not unwrap. A caller
// reaching for it gets false for a file that is plainly absent, and the
// missing-file branch it guards never runs.
func IsNotExist(err error) bool {
	return errors.Is(err, fs.ErrNotExist)
}
