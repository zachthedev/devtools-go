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
	// file (e.g., "go tool testpair update").
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
	// Error names a failure that stopped the check before it compared
	// anything. Without it a consumer reading stdout sees the same empty
	// report for a repository with no drift and a run that never happened.
	Error string `json:"error,omitempty"`
}

// invocation is a tool's command line after parsing.
type invocation struct {
	Mode     string
	Opts     Options
	Patterns []string
}

// ///////////////////////////////////////////////
// Constants
// ///////////////////////////////////////////////

// The modes a command line can select. Check is the default, so a bare
// invocation with no subcommand runs one.
const (
	modeCheck    = "check"
	modeUpdate   = "update"
	modeValidate = "validate"
	modeHelp     = "help"
)

// ///////////////////////////////////////////////
// Entry point
// ///////////////////////////////////////////////

// Main is the canonical entry point for a tool's main(). It parses argv,
// dispatches to the chosen subcommand, and calls os.Exit with the right
// code.
func Main[T fmt.Stringer](t *Tool[T]) {
	inv, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n\n", err)
		printUsage(t.Name)
		os.Exit(2)
	}
	switch inv.Mode {
	case modeHelp:
		printUsage(t.Name)
	case modeUpdate:
		t.RunUpdate(inv.Patterns)
	case modeValidate:
		t.RunValidate(inv.Patterns)
	case modeCheck:
		t.RunCheck(inv.Patterns, inv.Opts)
	}
}

// ///////////////////////////////////////////////
// Tool methods
// ///////////////////////////////////////////////

// RunUpdate regenerates the allow file from the current findings.
//
// Two things carry over from the file already there. Entries outside the
// patterns are kept, because a scoped run reads part of the module and
// deleting the rest would punish them for not having been looked for. Tags
// on entries that are still findings are kept too, so classifying an entry
// is something a maintainer does once. A line nobody has recorded arrives
// untagged, which is the rubber-stamped addition the check exists to stop.
func (t *Tool[T]) RunUpdate(patterns []string) {
	actual := t.Gather(patterns)
	inScope := t.lineInScope(patterns)

	prior := map[string]string{}
	var retained []allowlist.Entry

	existing, err := allowlist.LoadEntries(t.AllowFile, t.Categories)
	switch {
	case err != nil && !allowlist.IsNotExist(err):
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	case err == nil:
		for _, e := range existing {
			prior[e.Line] = e.Tag
			if !inScope(e.Line) {
				retained = append(retained, e)
			}
		}
	}

	entries := make([]allowlist.Entry, 0, len(actual)+len(retained))
	for _, x := range actual {
		line := x.String()
		entries = append(entries, allowlist.Entry{Tag: prior[line], Line: line})
	}
	entries = append(entries, retained...)

	if err := allowlist.WriteUpdate(t.AllowFile, t.Title, t.UpdateCmd, t.Categories, entries); err != nil {
		fmt.Fprintf(os.Stderr, "error: writing %s: %v\n", t.AllowFile, err)
		os.Exit(2)
	}

	report.PrintUpdated(os.Stderr, len(entries), t.AllowFile)
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
// falls within the patterns. It exits 2 when the patterns cannot be
// resolved, because a scope nobody established filters every entry and
// leaves the check comparing two empty sets.
func (t *Tool[T]) lineInScope(patterns []string) func(line string) bool {
	dirs, err := scope.PackageDirs(patterns)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
	inScope := scope.Matcher(dirs)
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

	uncat, err := allowlist.Validate(t.AllowFile, t.Categories, t.lineInScope(patterns))
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
		uncat, vErr := allowlist.Validate(t.AllowFile, t.Categories, inScope)
		if vErr != nil {
			fmt.Fprintln(os.Stderr, vErr)
			t.failJSON(opts, vErr.Error())
			os.Exit(2)
		}
		if len(uncat) > 0 {
			report.PrintUncategorized(os.Stderr, uncat, t.AllowFile)
			t.failJSON(opts, fmt.Sprintf("%s: %s with no [category] tag",
				t.AllowFile, report.Count(len(uncat), "entry", "entries")))
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
		report.WriteJSONTo(w, Report[T]{
			New:     report.Coalesce(newItems),
			Removed: report.Coalesce(removedItems),
			Total:   len(actual),
		})
		// Closed here rather than deferred, because os.Exit runs no
		// deferred call and the flush error would go unreported on exactly
		// the run that reports drift.
		closer()
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

// failJSON writes a report carrying only an error, for a failure that
// stops the check before it compares anything. It does nothing unless
// --json was asked for, so the text path is unaffected.
func (t *Tool[T]) failJSON(opts Options, msg string) {
	if !opts.JSON {
		return
	}
	w, closer := report.OpenJSONOutput(opts.JSONPath)
	report.WriteJSONTo(w, Report[T]{New: []T{}, Removed: []T{}, Error: msg})
	closer()
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

// parseArgs turns a tool's arguments into an [invocation].
//
// An argument that starts with a dash and names no flag above is an error.
// A go-list pattern never begins with one, so treating it as a pattern
// would hand a misspelled flag to go list and report it as a package that
// does not exist.
func parseArgs(argv []string) (invocation, error) {
	inv := invocation{Mode: modeCheck}
	for _, arg := range argv {
		switch {
		case arg == modeUpdate:
			inv.Mode = modeUpdate
		case arg == modeValidate:
			inv.Mode = modeValidate
		case arg == "--json":
			inv.Opts.JSON = true
		case strings.HasPrefix(arg, "--json="):
			inv.Opts.JSON = true
			inv.Opts.JSONPath = strings.TrimPrefix(arg, "--json=")
		case arg == "--quiet":
			inv.Opts.Quiet = true
		case arg == "--diff":
			inv.Opts.Diff = true
		case arg == "--help", arg == "-h":
			// Help answers the whole command line, so nothing after it
			// can select a mode that would run instead.
			return invocation{Mode: modeHelp}, nil
		case strings.HasPrefix(arg, "-"):
			return invocation{}, fmt.Errorf("unknown flag %s", arg)
		default:
			inv.Patterns = append(inv.Patterns, arg)
		}
	}
	if len(inv.Patterns) == 0 {
		inv.Patterns = []string{"./..."}
	}
	// Defensive: Diff + JSON together is ambiguous; the JSON wire shape
	// already carries enough for a consumer to compute a diff locally.
	// Prefer JSON if both are set.
	if inv.Opts.JSON {
		inv.Opts.Diff = false
	}
	return inv, nil
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
