// Package driver is the shared runtime for convention-enforcing devtools
// in this module. Each tool declares a [Tool] describing its allow-file
// name, category set, and callbacks for gathering findings and loading
// the allow list; the driver handles argument parsing, scoping,
// allow-list validation, diffing, reporting, and exit codes.
//
// A complete tool is defined by its [Tool] factory plus Gather/LoadAllow/
// ToFinding implementations. Dispatchers (and tool mains) invoke [Main].
//
// # Subcommands
//
//   - (default) check findings against the allow list
//   - update    regenerate the allow list
//   - validate  check the allow-file format only (no package scan)
//
// # Flags
//
//   - --json / --json=PATH  machine-readable diff report
//   - --quiet               suppress clean-pass line
//   - --diff                render findings as a +/- diff
//   - --help, -h            show usage
//
// # Exit codes
//
//   - 0  no new findings
//   - 1  policy drift (new findings or uncategorized entries)
//   - 2  invocation error (missing tool, bad flag, I/O failure)
package driver

import (
	"fmt"
	"os"
	"strings"

	"zach.tools/go/devtools/internal/allowlist"
	"zach.tools/go/devtools/internal/report"
	"zach.tools/go/devtools/internal/scope"
)

// ///////////////////////////////////////////////
// Types
// ///////////////////////////////////////////////

// Tool describes a concrete convention-enforcement tool. T is the tool's
// domain type for findings (e.g., testpair's Issue, deadcode's Entry).
type Tool[T fmt.Stringer] struct {
	// Name is the tool's invocation form ("testpair", "deadcode") used in
	// help text and usage lines.
	Name string
	// Title is the human-readable tool name used in the allow-file header
	// (e.g., "Test pairing", "Deadcode").
	Title string
	// AllowFile is the repo-relative path to the tool's allow file.
	AllowFile string
	// UpdateCmd is the command shown to users to regenerate the allow
	// file (e.g., "go tool devtools testpair update").
	UpdateCmd string
	// Categories are the valid allow-list category tags.
	Categories []allowlist.Category
	// RequireAllowFile controls behavior when AllowFile is absent: true
	// makes that a hard error, false treats it as an empty allow list.
	RequireAllowFile bool

	// Gather builds the current set of findings from patterns.
	Gather func(patterns []string) []T
	// LoadAllow reads the allow file into the tool's domain type.
	LoadAllow func() []T
	// ToFinding converts a domain entry into the unified [report.Finding]
	// shape used by text output.
	ToFinding func(T) report.Finding
	// AllowPath reports the repo-relative file path inside one allow-file
	// line, which is what scope resolution narrows on. Each tool states it
	// because each writes a different line: testpair "kind path detail",
	// deadcode "path func". A tool that leaves this nil resolves every
	// entry as the repo root, which is in scope for any pattern.
	AllowPath func(line string) string
}

// Options captures parsed CLI flags affecting presentation.
type Options struct {
	JSON     bool
	JSONPath string
	Quiet    bool
	Diff     bool
}

// Report is the JSON wire shape shared by every tool. The domain type T
// is the tool's own entry; downstream consumers parse the JSON against
// their own schema.
type Report[T any] struct {
	New     []T `json:"new"`
	Removed []T `json:"removed"`
	Total   int `json:"total"`
}

// ///////////////////////////////////////////////
// Entry point
// ///////////////////////////////////////////////

// Main is the canonical entry point for a tool's main(). It parses argv,
// dispatches to the chosen subcommand, and calls os.Exit with the right
// code.
func Main[T fmt.Stringer](t *Tool[T]) {
	mode, opts, patterns := parseArgs(t.Name)
	switch mode {
	case "update":
		t.RunUpdate(patterns)
	case "validate":
		t.RunValidate(patterns)
	case "check":
		t.RunCheck(patterns, opts)
	}
}

// ///////////////////////////////////////////////
// Tool methods
// ///////////////////////////////////////////////

// RunUpdate regenerates the allow file from the current findings.
func (t *Tool[T]) RunUpdate(patterns []string) {
	actual := t.Gather(patterns)

	lines := make([]string, len(actual))
	for i, x := range actual {
		lines[i] = x.String()
	}

	if err := allowlist.WriteUpdate(t.AllowFile, t.Title, t.UpdateCmd, t.Categories, lines); err != nil {
		fmt.Fprintf(os.Stderr, "error: writing %s: %v\n", t.AllowFile, err)
		os.Exit(2)
	}

	report.PrintUpdated(os.Stderr, len(actual), t.AllowFile)
}

// allowPath reports the file path in one allow-file line, defaulting to
// the repo root for a tool that named no accessor. The root is in scope for
// every pattern, so a tool that says nothing keeps its entries rather than
// having them silently filtered away.
func (t *Tool[T]) allowPath(line string) string {
	if t.AllowPath == nil {
		return "."
	}
	return t.AllowPath(line)
}

// lineInScope returns a predicate reporting whether one allow-file line
// falls within the patterns.
func (t *Tool[T]) lineInScope(patterns []string) func(line string) bool {
	inScope := scope.Matcher(scope.PackageDirs(patterns))
	return func(line string) bool { return inScope(t.allowPath(line)) }
}

// RunValidate checks the allow file's categorization without scanning
// the repository. Fast and side-effect-free; useful for pre-commit hooks.
func (t *Tool[T]) RunValidate(patterns []string) {
	if _, err := os.Stat(t.AllowFile); err != nil {
		if t.RequireAllowFile {
			fmt.Fprintf(os.Stderr, "error: %s not found; run '%s' to create it\n", t.AllowFile, t.UpdateCmd)
			os.Exit(1)
		}
		// No allow file and not required = trivially valid.
		fmt.Fprintf(os.Stderr, "%s: no allow file present, nothing to validate\n", t.Name)
		return
	}

	uncat, err := allowlist.Validate(t.AllowFile, t.lineInScope(patterns))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if len(uncat) > 0 {
		report.PrintUncategorized(os.Stderr, uncat, t.AllowFile)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "%s: allow file OK\n", t.AllowFile)
}

// RunCheck compares current findings against the allow list and fails on drift.
func (t *Tool[T]) RunCheck(patterns []string, opts Options) {
	if _, err := os.Stat(t.AllowFile); err != nil {
		if t.RequireAllowFile {
			fmt.Fprintf(os.Stderr, "error: %s not found; run '%s' to create it\n", t.AllowFile, t.UpdateCmd)
			os.Exit(1)
		}
		// Otherwise fall through with an empty allow list.
	}

	inScope := t.lineInScope(patterns)

	if _, err := os.Stat(t.AllowFile); err == nil {
		uncat, vErr := allowlist.Validate(t.AllowFile, inScope)
		if vErr != nil {
			fmt.Fprintln(os.Stderr, vErr)
			os.Exit(2)
		}
		if len(uncat) > 0 {
			report.PrintUncategorized(os.Stderr, uncat, t.AllowFile)
			os.Exit(1)
		}
	}

	actual := t.Gather(patterns)
	allowed := scope.Filter(t.LoadAllow(), func(entry T) string {
		// String is the canonical allow-file line for an entry, so one
		// accessor answers for both the parsed entries and the raw file.
		return t.allowPath(entry.String())
	}, inScope)

	newItems, removedItems := diff(actual, allowed)

	if opts.JSON {
		w, closer := report.OpenJSONOutput(opts.JSONPath)
		defer closer()
		report.WriteJSONTo(w, Report[T]{
			New:     report.Coalesce(newItems),
			Removed: report.Coalesce(removedItems),
			Total:   len(actual),
		})
		if len(newItems) > 0 {
			os.Exit(1)
		}
		return
	}

	exitCode := 0

	if opts.Diff {
		if len(newItems) > 0 || len(removedItems) > 0 {
			report.PrintDiff(os.Stderr, t.findings(newItems), t.findings(removedItems), t.AllowFile, t.UpdateCmd)
			if len(newItems) > 0 {
				exitCode = 1
			}
		} else if !opts.Quiet {
			report.PrintCleanPass(os.Stderr, len(actual), t.AllowFile)
		}
		os.Exit(exitCode)
	}

	if len(newItems) > 0 {
		report.PrintFindings(os.Stderr, t.findings(newItems), t.AllowFile)
		exitCode = 1
	}

	if len(removedItems) > 0 {
		if exitCode != 0 {
			fmt.Fprintln(os.Stderr)
		}
		report.PrintRemoved(os.Stderr, t.findings(removedItems), t.AllowFile, t.UpdateCmd)
	}

	if exitCode == 0 && len(removedItems) == 0 && !opts.Quiet {
		report.PrintCleanPass(os.Stderr, len(actual), t.AllowFile)
	}

	os.Exit(exitCode)
}

// findings maps the tool's domain items through ToFinding.
func (t *Tool[T]) findings(items []T) []report.Finding {
	out := make([]report.Finding, len(items))
	for i, x := range items {
		out[i] = t.ToFinding(x)
	}
	return out
}

// ///////////////////////////////////////////////
// Internal helpers
// ///////////////////////////////////////////////

// diff splits (actual, allowed) into the new and removed slices by
// comparing String() membership.
func diff[T fmt.Stringer](actual, allowed []T) (newItems, removedItems []T) {
	actualSet := indexByString(actual)
	allowedSet := indexByString(allowed)

	for _, x := range actual {
		if _, ok := allowedSet[x.String()]; !ok {
			newItems = append(newItems, x)
		}
	}
	for _, x := range allowed {
		if _, ok := actualSet[x.String()]; !ok {
			removedItems = append(removedItems, x)
		}
	}
	return newItems, removedItems
}

// indexByString collects the String() forms of a slice into a presence set
// so the check flow can test membership in O(1).
func indexByString[T fmt.Stringer](items []T) map[string]struct{} {
	m := make(map[string]struct{}, len(items))
	for _, x := range items {
		m[x.String()] = struct{}{}
	}
	return m
}

// parseArgs extracts mode, flags, and package patterns from os.Args.
// toolName is used in the --help output.
func parseArgs(toolName string) (mode string, opts Options, patterns []string) {
	mode = "check"
	for _, arg := range os.Args[1:] {
		switch {
		case arg == "update":
			mode = "update"
		case arg == "validate":
			mode = "validate"
		case arg == "--json":
			opts.JSON = true
		case strings.HasPrefix(arg, "--json="):
			opts.JSON = true
			opts.JSONPath = strings.TrimPrefix(arg, "--json=")
		case arg == "--quiet":
			opts.Quiet = true
		case arg == "--diff":
			opts.Diff = true
		case arg == "--help", arg == "-h":
			printUsage(toolName)
			os.Exit(0)
		default:
			patterns = append(patterns, arg)
		}
	}
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}
	// Defensive: Diff + JSON together is ambiguous; the JSON wire shape
	// already carries enough for a consumer to compute a diff locally.
	// Prefer JSON if both are set.
	if opts.JSON {
		opts.Diff = false
	}
	return mode, opts, patterns
}

// printUsage writes the standard help text for a tool.
func printUsage(toolName string) {
	fmt.Fprintf(os.Stderr, `Usage: %s [subcommand] [flags] [package-patterns...]

Subcommands:
  (default)    Check findings against the allow list.
  update       Regenerate the allow list from current findings.
  validate     Check the allow-file format only (no package scan).

Flags:
  --json       Emit machine-readable diff report to stdout.
  --json=PATH  Emit machine-readable diff report to PATH.
  --quiet      Suppress the clean-pass summary line.
  --diff       Render findings as a unified +/- diff.
  --help, -h   Show this help.

Package patterns use go-list syntax (e.g., ./cmd/... ./internal/...).
Defaults to ./... Scoped runs skip out-of-scope entries in the allow list
and ignore out-of-scope uncategorized tags.

Exit codes:
  0  no new findings
  1  policy drift (new findings or uncategorized entries)
  2  invocation error (missing tool, bad flag, I/O failure)

Color output is automatic when stderr is a terminal. Honors NO_COLOR.
`, toolName)
}
