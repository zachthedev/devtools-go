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
	"errors"
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
		UpdateCmd: "go tool testpair update",
		Categories: []allowlist.Category{
			{Tag: "multi-file", Description: "subcommand files tested via main_test.go"},
			{Tag: "cross-pkg", Description: "test covers symbols from a different package"},
			{Tag: "convention", Description: "naming follows project convention, not strict Go idiom"},
			{Tag: "entry-point", Description: "package clause and a one-line main; behaviour tested in internal/"},
			{Tag: "scenario", Description: "integration/scenario test not tied to one symbol"},
		},
		Gather:    findAllIssues,
		LoadAllow: loadAllowed,
		ToFinding: func(i Issue) report.Finding {
			return report.Finding{Kind: i.Kind, File: i.File, Detail: i.Message}
		},
		AllowPath: allowPath,
	}
}

// allowPath reports the file path in one .allow.testpair line, whose shape
// is "<kind> <file> <message>". The path comes second here and first in
// deadcode's file, which is why each tool states its own.
func allowPath(line string) string {
	parts := strings.SplitN(line, " ", 3)
	if len(parts) < 2 {
		return line
	}
	return parts[1]
}

// ///////////////////////////////////////////////
// Analysis
// ///////////////////////////////////////////////

// findAllIssues runs all three checks in parallel and returns sorted
// findings. Each check is independent, so missing-test and orphan-test
// walk the same dir maps while name-mismatch parses the test files.
//
// A file this cannot read or parse ends the run. Both halves of the
// name-mismatch check read source to build the symbol set it compares
// against, so an unreadable file shrinks that set and turns matching test
// names into findings. A whole directory that fails to parse drops out of
// the check instead, which reports nothing at all.
func findAllIssues(patterns []string) []Issue {
	pkgDirs, err := scope.PackageDirs(patterns)
	if err != nil {
		refuse(err)
	}
	sourceByDir := map[string][]string{}
	testByDir := map[string][]string{}

	var readErrs []error
	for _, dir := range pkgDirs {
		if err := collectFiles(dir, sourceByDir, testByDir); err != nil {
			readErrs = append(readErrs, err)
		}
	}
	if len(readErrs) > 0 {
		refuse(errors.Join(readErrs...))
	}

	var (
		wg                              sync.WaitGroup
		missing, orphan, nameMismatches []Issue
		parseErrs                       []error
	)
	wg.Go(func() { missing = findMissingTests(sourceByDir, testByDir) })
	wg.Go(func() { orphan = findOrphanTests(sourceByDir, testByDir) })
	wg.Go(func() { nameMismatches, parseErrs = findNamingMismatches(sourceByDir, testByDir) })
	wg.Wait()

	if len(parseErrs) > 0 {
		refuse(errors.Join(parseErrs...))
	}

	issues := slices.Concat(missing, orphan, nameMismatches)
	slices.SortFunc(issues, compareIssue)
	return issues
}

// refuse reports that the scan was incomplete and exits 2. It never
// returns.
//
// Findings from a partial read describe the files that happened to load,
// not the package, so reporting them would put a number on an answer the
// scan does not have.
func refuse(err error) {
	fmt.Fprintf(os.Stderr, "error: testpair could not read every file it needs:\n\n")
	for line := range strings.SplitSeq(err.Error(), "\n") {
		fmt.Fprintf(os.Stderr, "  %s\n", report.Block(line))
	}
	fmt.Fprintf(os.Stderr, "\nFix the listed files, or narrow the run to packages that parse.\n")
	os.Exit(2)
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
func collectFiles(dir string, sourceByDir, testByDir map[string][]string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("reading package directory %s: %w", dir, err)
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
	return nil
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
// any symbol in the package. It returns the parse failures it hit alongside
// the findings, because a symbol set built from a partial read produces
// mismatches that say more about the reader than the code.
func findNamingMismatches(sourceByDir, testByDir map[string][]string) ([]Issue, []error) {
	var (
		issues []Issue
		errs   []error
	)
	for dir, tests := range testByDir {
		// A directory holding no source at all is already answered by the
		// orphan-test check, and its test names have nothing to match
		// against. A directory that does hold source is checked even when
		// that source declares nothing, because an empty symbol set means
		// every test name in it matches nothing.
		if len(sourceByDir[dir]) == 0 {
			continue
		}
		pkgSymbols, symErrs := buildSymbolSet(dir, sourceByDir[dir])
		errs = append(errs, symErrs...)

		for _, test := range tests {
			testPath := filepath.Join(dir, test)
			funcs, err := extractTestFuncs(testPath)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			for _, funcName := range funcs {
				base := extractTestBase(funcName)
				if base == "" {
					// `func Test` is a legal test that names no symbol, so
					// the convention has nothing to hold it to.
					issues = append(issues, Issue{
						Kind:    "name-mismatch",
						File:    dir + "/" + test,
						Message: funcName + ": names no symbol to test",
					})
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
	return issues, errs
}

// ///////////////////////////////////////////////
// AST Helpers
// ///////////////////////////////////////////////

// buildSymbolSet collects all function, method, type, var, and const names
// from every source file in a package directory. Includes capitalized forms
// of unexported names so TestFoo matches an unexported foo.
func buildSymbolSet(dir string, sources []string) (map[string]struct{}, []error) {
	symbols := map[string]struct{}{}
	fset := token.NewFileSet()

	var errs []error
	for _, src := range sources {
		path := filepath.Join(dir, src)
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			errs = append(errs, fmt.Errorf("parsing %s: %w", path, err))
			continue
		}
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				addSymbol(symbols, d.Name.Name)
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						addSymbol(symbols, s.Name.Name)
					case *ast.ValueSpec:
						for _, n := range s.Names {
							addSymbol(symbols, n.Name)
						}
					}
				}
			}
		}
	}
	return symbols, errs
}

// addSymbol records a declared name under both the spelling it was
// declared with and its capitalized form, so a test for an unexported
// symbol can name it the way a test function must start.
func addSymbol(symbols map[string]struct{}, name string) {
	symbols[name] = struct{}{}
	symbols[upperFirst(name)] = struct{}{}
}

// extractTestFuncs returns all Test* function names from a test file.
func extractTestFuncs(path string) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}

	var names []string
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Recv != nil {
			continue
		}
		if strings.HasPrefix(fd.Name.Name, "Test") && !isTestMain(fd) {
			names = append(names, fd.Name.Name)
		}
	}
	return names, nil
}

// isTestMain reports whether a declaration is the test binary entry point,
// which go test calls in place of running the tests itself.
//
// It is named for the role it plays rather than for a symbol under test, so
// holding it to the TestSymbol_* convention would ask every package that
// defines one to declare a symbol named Main. The parameter type is what
// separates it from an ordinary test of a symbol called Main.
func isTestMain(fd *ast.FuncDecl) bool {
	if fd.Name.Name != "TestMain" || fd.Type.Params == nil || len(fd.Type.Params.List) != 1 {
		return false
	}
	star, ok := fd.Type.Params.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "testing" && sel.Sel.Name == "M"
}

// extractTestBase returns the symbol name from a test function.
// TestFoo_Bar_Baz returns "Foo"; TestFoo returns "Foo".
func extractTestBase(name string) string {
	suffix := strings.TrimPrefix(name, "Test")
	if base, _, found := strings.Cut(suffix, "_"); found && base != "" {
		return base
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
		// Missing file is fine; treat as empty allow list. The check is
		// through allowlist because the errors here are wrapped, and
		// os.IsNotExist does not unwrap: it answers false for a file that
		// is plainly absent, and this branch would never run.
		if allowlist.IsNotExist(err) {
			return nil
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}

	var (
		issues    []Issue
		malformed []string
	)
	for _, line := range lines {
		parts := strings.SplitN(line, " ", 3)
		if len(parts) != 3 {
			malformed = append(malformed, line)
			continue
		}
		issues = append(issues, Issue{Kind: parts[0], File: parts[1], Message: parts[2]})
	}
	// Skipping a line here would shrink the allow list without saying so,
	// and the exception count printed on a clean pass would then disagree
	// with the file it names.
	if len(malformed) > 0 {
		report.PrintMalformed(os.Stderr, malformed, allowFile)
		os.Exit(2)
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
	line, _, _ := strings.Cut(string(buf[:n]), "\n")
	return strings.Contains(line, "Auto-generated") ||
		strings.Contains(line, "Code generated") ||
		strings.Contains(line, "DO NOT EDIT")
}

// ///////////////////////////////////////////////
// Utilities
// ///////////////////////////////////////////////

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
