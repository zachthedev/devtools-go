// Test file pairing and naming enforcement for Go projects.
//
// Checks two conventions:
//  1. Every source file has a _test.go companion (and vice versa)
//  2. Test function names match TestSymbol_* where Symbol exists
//     in any source file in the same package
//
// Findings are compared against .testpair-allow. Only new violations
// fail; documented exceptions are accepted. Every allow-list entry must
// have a [category] tag to prevent rubber-stamping.
//
// Usage:
//
//	go tool testpair ./cmd/... ./internal/...         check
//	go tool testpair update ./cmd/... ./internal/...  regenerate allow list
//	go tool testpair --json ./cmd/... ./internal/...  JSON output
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"zach.tools/go/devtools/allowlist"
)

// ///////////////////////////////////////////////
// Types
// ///////////////////////////////////////////////

// Issue represents a single convention violation.
type Issue struct {
	Kind    string `json:"kind"`
	File    string `json:"file"`
	Message string `json:"message"`
}

// Report is the top-level output structure.
type Report struct {
	New     []Issue `json:"new"`
	Removed []Issue `json:"removed"`
	Total   int     `json:"total"`
}

// ///////////////////////////////////////////////
// Constants
// ///////////////////////////////////////////////

const allowFile = ".allow.testpair"

var categories = []allowlist.Category{
	{Tag: "multi-file", Description: "subcommand files tested via main_test.go"},
	{Tag: "cross-pkg", Description: "test covers symbols from a different package"},
	{Tag: "convention", Description: "naming follows project convention, not strict Go idiom"},
	{Tag: "scenario", Description: "integration/scenario test not tied to one symbol"},
}

// String returns the canonical form used in .allow.testpair.
func (i Issue) String() string { return i.Kind + " " + i.File + " " + i.Message }

// ///////////////////////////////////////////////
// Entry Point
// ///////////////////////////////////////////////

func main() {
	mode := "check"
	jsonOutput := false
	var patterns []string

	for _, arg := range os.Args[1:] {
		switch arg {
		case "update":
			mode = "update"
		case "--json":
			jsonOutput = true
		case "--help", "-h":
			fmt.Fprintln(os.Stderr, "usage: go tool testpair [update] [--json] [package-patterns...]")
			fmt.Fprintln(os.Stderr, "")
			fmt.Fprintln(os.Stderr, "Accepts go list patterns (e.g., ./cmd/... ./internal/...).")
			fmt.Fprintln(os.Stderr, "Defaults to ./... if no patterns given.")
			os.Exit(0)
		default:
			patterns = append(patterns, arg)
		}
	}

	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}

	switch mode {
	case "update":
		runUpdate(patterns)
	case "check":
		runCheck(patterns, jsonOutput)
	}
}

// ///////////////////////////////////////////////
// Commands
// ///////////////////////////////////////////////

// runUpdate regenerates .testpair-allow from the current findings.
func runUpdate(patterns []string) {
	actual := findAllIssues(patterns)

	var lines []string
	for _, iss := range actual {
		lines = append(lines, iss.String())
	}

	if err := allowlist.WriteUpdate(allowFile, "Test pairing", "make testpair ARGS=update", categories, lines); err != nil {
		fmt.Fprintf(os.Stderr, "error: writing %s: %v\n", allowFile, err)
		os.Exit(2)
	}

	fmt.Fprintf(os.Stderr, "Allow list updated: %d entries in %s\n", len(actual), allowFile) //nolint:gosec // stderr, not HTTP
}

// runCheck compares current findings against the allow list and fails on drift.
func runCheck(patterns []string, jsonOutput bool) {
	if _, err := os.Stat(allowFile); err == nil {
		allowlist.FailOnUncategorized(allowFile)
	}

	actual := findAllIssues(patterns)
	allowed := loadAllowed()

	actualSet := issueSet(actual)
	allowedSet := issueSet(allowed)

	var newIssues, removedIssues []Issue
	for _, iss := range actual {
		if !allowedSet[iss.String()] {
			newIssues = append(newIssues, iss)
		}
	}
	for _, iss := range allowed {
		if !actualSet[iss.String()] {
			removedIssues = append(removedIssues, iss)
		}
	}

	if jsonOutput {
		allowlist.WriteJSON(Report{
			New:     allowlist.Coalesce(newIssues),
			Removed: allowlist.Coalesce(removedIssues),
			Total:   len(actual),
		})
		if len(newIssues) > 0 {
			os.Exit(1)
		}
		return
	}

	exitCode := 0

	if len(newIssues) > 0 {
		fmt.Fprintf(os.Stderr, "\nFAIL: %d new test pairing issue(s) not in allow list:\n", len(newIssues)) //nolint:gosec // stderr, not HTTP
		printGrouped(newIssues)
		fmt.Fprintf(os.Stderr, "\nFix the issue or add to %s with a category tag.\n", allowFile)
		exitCode = 1
	}

	if len(removedIssues) > 0 {
		fmt.Fprintf(os.Stderr, "\n%d allow-list entries are no longer violations:\n", len(removedIssues))
		printGrouped(removedIssues)
		fmt.Fprintf(os.Stderr, "\nRun 'make testpair ARGS=update' to shrink the allow list.\n")
	}

	if exitCode == 0 && len(removedIssues) == 0 {
		fmt.Fprintf(os.Stderr, "%d known exceptions, no new violations.\n", len(actual)) //nolint:gosec // stderr, not HTTP
	}

	os.Exit(exitCode)
}

// ///////////////////////////////////////////////
// Analysis
// ///////////////////////////////////////////////

// findAllIssues runs all checks and returns sorted findings.
func findAllIssues(patterns []string) []Issue {
	pkgDirs := listPackageDirs(patterns)
	sourceByDir := map[string][]string{}
	testByDir := map[string][]string{}

	for _, dir := range pkgDirs {
		collectFiles(dir, sourceByDir, testByDir)
	}

	var issues []Issue
	issues = append(issues, findMissingTests(sourceByDir, testByDir)...)
	issues = append(issues, findOrphanTests(sourceByDir, testByDir)...)
	issues = append(issues, findNamingMismatches(sourceByDir, testByDir)...)

	sort.Slice(issues, func(i, j int) bool {
		if issues[i].Kind != issues[j].Kind {
			return issues[i].Kind < issues[j].Kind
		}
		if issues[i].File != issues[j].File {
			return issues[i].File < issues[j].File
		}
		return issues[i].Message < issues[j].Message
	})
	return issues
}

// ///////////////////////////////////////////////
// Package Discovery
// ///////////////////////////////////////////////

// listPackageDirs uses `go list` to resolve package patterns into directories.
func listPackageDirs(patterns []string) []string {
	args := append([]string{"list", "-f", "{{.Dir}}"}, patterns...)
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

	seen := map[string]bool{}
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
		if !seen[rel] {
			seen[rel] = true
			dirs = append(dirs, rel)
		}
	}
	sort.Strings(dirs)
	return dirs
}

// ///////////////////////////////////////////////
// File Collection
// ///////////////////////////////////////////////

// collectFiles reads a single directory and groups .go files into source
// and test buckets.
func collectFiles(dir string, sourceByDir, testByDir map[string][]string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		if strings.HasSuffix(name, "_test.go") {
			testByDir[dir] = append(testByDir[dir], name)
		} else {
			sourceByDir[dir] = append(sourceByDir[dir], name)
		}
	}
}

// ///////////////////////////////////////////////
// Checks
// ///////////////////////////////////////////////

// findMissingTests flags source files without a _test.go companion.
func findMissingTests(sourceByDir, testByDir map[string][]string) []Issue {
	var issues []Issue
	for dir, sources := range sourceByDir {
		for _, src := range sources {
			if isGenerated(filepath.Join(dir, src)) {
				continue
			}
			testName := strings.TrimSuffix(src, ".go") + "_test.go"
			if !slices.Contains(testByDir[dir], testName) {
				issues = append(issues, Issue{
					Kind:    "missing-test",
					File:    dir + "/" + src,
					Message: "expected " + dir + "/" + testName,
				})
			}
		}
	}
	return issues
}

// findOrphanTests flags test files without a corresponding source file.
func findOrphanTests(sourceByDir, testByDir map[string][]string) []Issue {
	var issues []Issue
	for dir, tests := range testByDir {
		for _, test := range tests {
			srcName := strings.TrimSuffix(test, "_test.go") + ".go"
			if !slices.Contains(sourceByDir[dir], srcName) {
				issues = append(issues, Issue{
					Kind:    "orphan-test",
					File:    dir + "/" + test,
					Message: "no source file " + dir + "/" + srcName,
				})
			}
		}
	}
	return issues
}

// findNamingMismatches flags test functions whose name prefix does not match
// any symbol in the package.
func findNamingMismatches(sourceByDir, testByDir map[string][]string) []Issue {
	var issues []Issue
	for dir, tests := range testByDir {
		pkgSymbols := buildSymbolSet(dir, sourceByDir[dir])
		if len(pkgSymbols) == 0 {
			continue
		}

		for _, test := range tests {
			testPath := filepath.Join(dir, test)
			for _, funcName := range extractTestFuncs(testPath) {
				base := extractTestBase(funcName)
				if base == "" {
					continue
				}
				if !pkgSymbols[base] {
					issues = append(issues, Issue{
						Kind:    "name-mismatch",
						File:    dir + "/" + test,
						Message: funcName + ": no symbol '" + base + "' in package",
					})
				}
			}
		}
	}
	return issues
}

// ///////////////////////////////////////////////
// AST Helpers
// ///////////////////////////////////////////////

// buildSymbolSet collects all function, method, type, var, and const names
// from every source file in a package directory. Includes capitalized forms
// of unexported names so TestFoo matches an unexported foo.
func buildSymbolSet(dir string, sources []string) map[string]bool {
	symbols := map[string]bool{}
	fset := token.NewFileSet()

	for _, src := range sources {
		f, err := parser.ParseFile(fset, filepath.Join(dir, src), nil, 0)
		if err != nil {
			continue
		}
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				symbols[d.Name.Name] = true
				symbols[upperFirst(d.Name.Name)] = true
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						symbols[s.Name.Name] = true
						symbols[upperFirst(s.Name.Name)] = true
					case *ast.ValueSpec:
						for _, n := range s.Names {
							symbols[n.Name] = true
							symbols[upperFirst(n.Name)] = true
						}
					}
				}
			}
		}
	}
	return symbols
}

// extractTestFuncs returns all Test* function names from a test file.
func extractTestFuncs(path string) []string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil
	}

	var names []string
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Recv != nil {
			continue
		}
		if strings.HasPrefix(fd.Name.Name, "Test") {
			names = append(names, fd.Name.Name)
		}
	}
	return names
}

// extractTestBase returns the symbol name from a test function.
// TestFoo_Bar_Baz returns "Foo"; TestFoo returns "Foo".
func extractTestBase(name string) string {
	suffix := strings.TrimPrefix(name, "Test")
	if idx := strings.Index(suffix, "_"); idx > 0 {
		return suffix[:idx]
	}
	return suffix
}

// ///////////////////////////////////////////////
// Allow List
// ///////////////////////////////////////////////

// loadAllowed reads .testpair-allow entries.
func loadAllowed() []Issue {
	lines, err := allowlist.LoadLines(allowFile)
	if err != nil {
		// Missing file is fine; treat as empty allow list.
		if os.IsNotExist(err) {
			return nil
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}

	var issues []Issue
	for _, line := range lines {
		parts := strings.SplitN(line, " ", 3)
		if len(parts) != 3 {
			continue
		}
		issues = append(issues, Issue{Kind: parts[0], File: parts[1], Message: parts[2]})
	}
	sort.Slice(issues, func(i, j int) bool {
		return issues[i].String() < issues[j].String()
	})
	return issues
}

func issueSet(issues []Issue) map[string]bool {
	m := make(map[string]bool, len(issues))
	for _, iss := range issues {
		m[iss.String()] = true
	}
	return m
}

// ///////////////////////////////////////////////
// File Helpers
// ///////////////////////////////////////////////

// isGenerated checks whether a file begins with a generated-code marker.
func isGenerated(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	buf := make([]byte, 256)
	n, _ := f.Read(buf)
	line := string(buf[:n])
	if idx := strings.Index(line, "\n"); idx > 0 {
		line = line[:idx]
	}
	return strings.Contains(line, "Auto-generated") ||
		strings.Contains(line, "Code generated") ||
		strings.Contains(line, "DO NOT EDIT")
}

// ///////////////////////////////////////////////
// Output
// ///////////////////////////////////////////////

func printGrouped(issues []Issue) {
	prevKind := ""
	for _, iss := range issues {
		if iss.Kind != prevKind {
			if prevKind != "" {
				fmt.Fprintln(os.Stderr)
			}
			fmt.Fprintf(os.Stderr, "  [%s]\n", iss.Kind) //nolint:gosec // stderr, not HTTP
			prevKind = iss.Kind
		}
		fmt.Fprintf(os.Stderr, "    %-50s %s\n", iss.File, iss.Message) //nolint:gosec // stderr, not HTTP
	}
}

// ///////////////////////////////////////////////
// Utilities
// ///////////////////////////////////////////////

func upperFirst(s string) string {
	if len(s) == 0 {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
