// Package testpair implements the testpair devtool. Checks two conventions:
//  1. Every source file has a _test.go companion (and vice versa).
//  2. Test function names match TestSymbol_* where Symbol exists in any
//     source file in the same package.
//
// Findings are compared against .allow.testpair. See the driver package
// for the check/update/validate/--json lifecycle.
package testpair

import (
	"cmp"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"zach.tools/go/devtools/internal/allowlist"
	"zach.tools/go/devtools/internal/driver"
	"zach.tools/go/devtools/internal/report"
	"zach.tools/go/devtools/internal/scope"
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

// ///////////////////////////////////////////////
// Constants
// ///////////////////////////////////////////////

const allowFile = ".allow.testpair"

// ///////////////////////////////////////////////
// Methods
// ///////////////////////////////////////////////

// String returns the canonical form used in .allow.testpair.
func (i Issue) String() string { return i.Kind + " " + i.File + " " + i.Message }

// ///////////////////////////////////////////////
// Tool
// ///////////////////////////////////////////////

// Tool returns a configured [driver.Tool] for the testpair subcommand.
// Call [driver.Main] on the result from a main package or dispatcher.
func Tool() *driver.Tool[Issue] {
	return &driver.Tool[Issue]{
		Name:      "testpair",
		Title:     "Test pairing",
		AllowFile: allowFile,
		UpdateCmd: "go tool devtools testpair update",
		Categories: []allowlist.Category{
			{Tag: "multi-file", Description: "subcommand files tested via main_test.go"},
			{Tag: "cross-pkg", Description: "test covers symbols from a different package"},
			{Tag: "convention", Description: "naming follows project convention, not strict Go idiom"},
			{Tag: "scenario", Description: "integration/scenario test not tied to one symbol"},
		},
		Gather:    findAllIssues,
		LoadAllow: loadAllowed,
		ToFinding: func(i Issue) report.Finding {
			return report.Finding{Kind: i.Kind, File: i.File, Detail: i.Message}
		},
	}
}

// ///////////////////////////////////////////////
// Analysis
// ///////////////////////////////////////////////

// findAllIssues runs all three checks in parallel and returns sorted
// findings. Each check is independent — missing-test and orphan-test walk
// the same dir maps, name-mismatch parses the test files — so they can run
// concurrently without shared-state contention.
func findAllIssues(patterns []string) []Issue {
	pkgDirs := scope.PackageDirs(patterns)
	sourceByDir := map[string][]string{}
	testByDir := map[string][]string{}

	for _, dir := range pkgDirs {
		collectFiles(dir, sourceByDir, testByDir)
	}

	var (
		wg                              sync.WaitGroup
		missing, orphan, nameMismatches []Issue
	)
	wg.Go(func() { missing = findMissingTests(sourceByDir, testByDir) })
	wg.Go(func() { orphan = findOrphanTests(sourceByDir, testByDir) })
	wg.Go(func() { nameMismatches = findNamingMismatches(sourceByDir, testByDir) })
	wg.Wait()

	issues := slices.Concat(missing, orphan, nameMismatches)
	slices.SortFunc(issues, compareIssue)
	return issues
}

// compareIssue orders issues by (Kind, File, Message) for stable output.
func compareIssue(a, b Issue) int {
	if c := cmp.Compare(a.Kind, b.Kind); c != 0 {
		return c
	}
	if c := cmp.Compare(a.File, b.File); c != 0 {
		return c
	}
	return cmp.Compare(a.Message, b.Message)
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
				if _, ok := pkgSymbols[base]; !ok {
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
func buildSymbolSet(dir string, sources []string) map[string]struct{} {
	symbols := map[string]struct{}{}
	fset := token.NewFileSet()

	for _, src := range sources {
		f, err := parser.ParseFile(fset, filepath.Join(dir, src), nil, 0)
		if err != nil {
			continue
		}
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				symbols[d.Name.Name] = struct{}{}
				symbols[upperFirst(d.Name.Name)] = struct{}{}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						symbols[s.Name.Name] = struct{}{}
						symbols[upperFirst(s.Name.Name)] = struct{}{}
					case *ast.ValueSpec:
						for _, n := range s.Names {
							symbols[n.Name] = struct{}{}
							symbols[upperFirst(n.Name)] = struct{}{}
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

// loadAllowed reads .allow.testpair entries.
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
	slices.SortFunc(issues, compareIssue)
	return issues
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
// Utilities
// ///////////////////////////////////////////////

func upperFirst(s string) string {
	if len(s) == 0 {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
