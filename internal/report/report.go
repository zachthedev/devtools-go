// Package report renders the standard output blocks shared by every
// devtool in this module: check summaries, failure listings, removed
// entries, update confirmations, diff views, and JSON reports.
//
// Tool-specific code constructs [Finding] slices from its own domain
// types and calls the printer; the printer owns formatting, alignment,
// pluralization, and color.
//
// All block printers write to an [io.Writer], typically os.Stderr so
// stdout stays free for --json output.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/fatih/color"
)

// ///////////////////////////////////////////////
// Types
// ///////////////////////////////////////////////

// Finding is the tool-agnostic shape of a single detection, used by
// [PrintFindings], [PrintRemoved], and [PrintDiff] to render aligned
// tables.
type Finding struct {
	// Kind categorizes the finding (e.g., "missing-test", "unreachable").
	// Shown as [kind] in the first column.
	Kind string
	// File is the source file path the finding refers to.
	File string
	// Detail is the short human-readable explanation.
	Detail string
}

// ///////////////////////////////////////////////
// Palette
// ///////////////////////////////////////////////

var (
	red    = color.New(color.FgRed).SprintFunc()
	green  = color.New(color.FgGreen).SprintFunc()
	bold   = color.New(color.Bold).SprintFunc()
	yellow = color.New(color.FgYellow).SprintFunc()
	faint  = color.New(color.Faint).SprintFunc()
)

// ///////////////////////////////////////////////
// Pluralization
// ///////////////////////////////////////////////

// Count formats a counted noun with correct singular/plural agreement.
// Pass plural = "" for the regular "singular + s" form.
//
//	Count(1, "finding", "")        // "1 finding"
//	Count(3, "finding", "")        // "3 findings"
//	Count(2, "entry", "entries")   // "2 entries"
func Count(n int, singular, plural string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", singular)
	}
	if plural == "" {
		plural = singular + "s"
	}
	return fmt.Sprintf("%d %s", n, plural)
}

// ///////////////////////////////////////////////
// Block printers
// ///////////////////////////////////////////////

// PrintCleanPass writes the single-line summary for a check with no new
// findings. allowFile is the path to the tool's allow file.
func PrintCleanPass(w io.Writer, known int, allowFile string) {
	fmt.Fprintf(w, "%s 0 new findings, %s in %s\n",
		green("OK"), Count(known, "known exception", ""), allowFile)
}

// PrintFindings writes the FAIL block for new findings not yet in the
// allow file. allowFile appears in both the heading and the fix-footer.
func PrintFindings(w io.Writer, findings []Finding, allowFile string) {
	fmt.Fprintf(w, "%s %s not in %s:\n\n",
		red(bold("FAIL:")),
		Count(len(findings), "new finding", ""),
		allowFile)
	writeTable(w, findings, "")
	fmt.Fprintf(w, "\nFix the finding or add it to %s with a category tag.\n", allowFile)
}

// PrintRemoved writes the block listing allow-list entries that are no
// longer findings. updateCmd is the invocation users should run to
// regenerate the allow file.
func PrintRemoved(w io.Writer, removed []Finding, allowFile, updateCmd string) {
	fmt.Fprintf(w, "%s in %s %s:\n\n",
		Count(len(removed), "allow-list entry", "allow-list entries"),
		allowFile, staleClause(len(removed)))
	writeTable(w, removed, "")
	fmt.Fprintf(w, "\nRun %s to shrink the allow list.\n", bold(updateCmd))
}

// PrintUpdated writes the update-mode confirmation.
func PrintUpdated(w io.Writer, count int, allowFile string) {
	fmt.Fprintf(w, "%s %s: %s\n", green("Updated"), allowFile, Count(count, "entry", "entries"))
}

// PrintUncategorized writes the FAIL block for allow-list entries lacking
// a category tag.
func PrintUncategorized(w io.Writer, entries []string, allowFile string) {
	fmt.Fprintf(w, "%s %s in %s %s no [category] tag:\n\n",
		red(bold("FAIL:")),
		Count(len(entries), "allow-list entry", "allow-list entries"),
		allowFile, verbHave(len(entries)))
	for _, line := range entries {
		fmt.Fprintf(w, "  %s\n", field(line))
	}
	fmt.Fprintf(w, "\nEvery entry needs a %s comment above its group.\n",
		bold("# [category]"))
}

// PrintDiff renders new findings with `+` prefixes and removed entries
// with `-` prefixes in a single unified block, like a patch hunk.
func PrintDiff(w io.Writer, newFindings, removed []Finding, allowFile, updateCmd string) {
	summary := fmt.Sprintf("%s: %s, %s",
		bold("diff"),
		Count(len(newFindings), "added", "added"),
		Count(len(removed), "removed", "removed"))
	fmt.Fprintln(w, summary)
	fmt.Fprintf(w, "%s %s\n\n", faint("---"), faint(allowFile))

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, f := range newFindings {
		fmt.Fprintf(tw, "%s\t[%s]\t%s\t%s\n", green("+"), field(f.Kind), field(f.File), field(f.Detail))
	}
	for _, f := range removed {
		fmt.Fprintf(tw, "%s\t[%s]\t%s\t%s\n", yellow("-"), field(f.Kind), field(f.File), field(f.Detail))
	}
	_ = tw.Flush()

	switch {
	case len(newFindings) > 0 && len(removed) > 0:
		fmt.Fprintf(w, "\nFix the additions or add them to %s; run %s to shrink.\n",
			allowFile, bold(updateCmd))
	case len(newFindings) > 0:
		fmt.Fprintf(w, "\nFix the additions or add them to %s with a category tag.\n", allowFile)
	case len(removed) > 0:
		fmt.Fprintf(w, "\nRun %s to shrink the allow list.\n", bold(updateCmd))
	}
}

// ///////////////////////////////////////////////
// JSON output
// ///////////////////////////////////////////////

// WriteJSONTo encodes a value as indented JSON to w. Exits the process
// on encoding error.
func WriteJSONTo(w io.Writer, v any) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fmt.Fprintf(os.Stderr, "error: encoding JSON: %v\n", err)
		os.Exit(2)
	}
}

// Coalesce returns an empty slice instead of nil so JSON serialization
// emits `[]` rather than `null` for empty result sets.
func Coalesce[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// OpenJSONOutput returns a writer for --json output and a closer to flush
// it. An empty path yields os.Stdout and a no-op closer; a non-empty path
// creates the file (exiting on failure) and returns a closer that closes
// it. Callers typically defer the closer.
func OpenJSONOutput(path string) (io.Writer, func()) {
	if path == "" {
		return os.Stdout, func() {}
	}
	f, err := os.Create(path) //nolint:gosec // caller-supplied flag path
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: creating %s: %v\n", path, err)
		os.Exit(2)
	}
	return f, func() {
		// Close on a write-file can return a flush error; surface it so
		// the user sees that their JSON report may be truncated.
		if cerr := f.Close(); cerr != nil {
			fmt.Fprintf(os.Stderr, "error: closing %s: %v\n", path, cerr)
		}
	}
}

// ///////////////////////////////////////////////
// Internal helpers
// ///////////////////////////////////////////////

// writeTable renders findings as an aligned 3-column table via tabwriter.
// Entries are indented two spaces; columns are padded to the widest row.
// prefix is prepended to every row after the indent (e.g., "+" for diff
// additions). An empty prefix produces the default block layout.
func writeTable(w io.Writer, findings []Finding, prefix string) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, f := range findings {
		kind, file, detail := field(f.Kind), field(f.File), field(f.Detail)
		if prefix == "" {
			fmt.Fprintf(tw, "  [%s]\t%s\t%s\n", kind, file, detail)
		} else {
			fmt.Fprintf(tw, "  %s\t[%s]\t%s\t%s\n", prefix, kind, file, detail)
		}
	}
	_ = tw.Flush()
}

// field renders one table cell, escaping every control character.
//
// A finding carries text from an analyzer and a path from the repository,
// and a pull request controls both. Printed raw, an escape sequence erases
// the row it sits on and redraws it as whatever the author chose, so a
// failing check can be made to read as a passing one. A tab is escaped for
// the duller reason that it would split the cell.
func field(s string) string {
	return escape(s, false)
}

// Block renders untrusted multi-line text for a stderr echo, escaping
// every control character except the newlines that give it its shape.
func Block(s string) string {
	return escape(s, true)
}

// escape replaces control characters with a readable \xNN form, optionally
// keeping newlines so a multi-line quote still reads as one.
func escape(s string, keepNewlines bool) string {
	if !strings.ContainsFunc(s, func(r rune) bool { return control(r, keepNewlines) }) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if control(r, keepNewlines) {
			fmt.Fprintf(&b, `\x%02x`, r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// control reports whether a rune would move the cursor rather than print.
func control(r rune, keepNewlines bool) bool {
	if keepNewlines && r == '\n' {
		return false
	}
	return r < 0x20 || r == 0x7f
}

// PrintMalformed writes the FAIL block for allow-file lines a tool cannot
// parse, and is the counterpart to refusing unreadable analyzer output.
// Skipping such a line would shrink the allow list without saying so, and
// the reported exception count would then disagree with the file.
func PrintMalformed(w io.Writer, lines []string, allowFile string) {
	fmt.Fprintf(w, "%s %s in %s cannot be parsed:\n\n",
		red(bold("FAIL:")),
		Count(len(lines), "line", ""),
		allowFile)
	for _, line := range lines {
		fmt.Fprintf(w, "  %s\n", field(line))
	}
	fmt.Fprintf(w, "\nEvery entry must match the shape this tool writes.\n")
}

// verbHave returns "has" for n == 1 and "have" otherwise, for constructing
// grammatically agreeing sentences.
func verbHave(n int) string {
	if n == 1 {
		return "has"
	}
	return "have"
}

// staleClause completes a sentence about entries that stopped being
// findings. The noun agrees along with the verb, so one entry is no longer
// a finding rather than no longer findings.
func staleClause(n int) string {
	if n == 1 {
		return "is no longer a finding"
	}
	return "are no longer findings"
}
