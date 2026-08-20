package driver

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"zach.tools/go/devtools/internal/allowlist"
	"zach.tools/go/devtools/internal/report"
)

// ///////////////////////////////////////////////
// Helpers
// ///////////////////////////////////////////////

// stringID is a fmt.Stringer for exercising generic helpers.
type stringID string

// updateCategories are the tags the RunUpdate fixtures are written against.
var updateCategories = []allowlist.Category{
	{Tag: "alpha", Description: "first"},
	{Tag: "beta", Description: "second"},
}

func (s stringID) String() string { return string(s) }

// ///////////////////////////////////////////////
// parseArgs
// ///////////////////////////////////////////////

func TestParseArgs_ReadsModeFlagsAndPatterns(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want invocation
	}{
		{
			name: "a bare command line checks every package",
			argv: nil,
			want: invocation{Mode: modeCheck, Patterns: []string{"./..."}},
		},
		{
			name: "update",
			argv: []string{"update"},
			want: invocation{Mode: modeUpdate, Patterns: []string{"./..."}},
		},
		{
			name: "validate",
			argv: []string{"validate"},
			want: invocation{Mode: modeValidate, Patterns: []string{"./..."}},
		},
		{
			name: "json to stdout",
			argv: []string{"--json"},
			want: invocation{
				Mode:     modeCheck,
				Opts:     Options{JSON: true},
				Patterns: []string{"./..."},
			},
		},
		{
			name: "json to a path",
			argv: []string{"--json=out.json"},
			want: invocation{
				Mode:     modeCheck,
				Opts:     Options{JSON: true, JSONPath: "out.json"},
				Patterns: []string{"./..."},
			},
		},
		{
			name: "quiet and diff together",
			argv: []string{"--quiet", "--diff"},
			want: invocation{
				Mode:     modeCheck,
				Opts:     Options{Quiet: true, Diff: true},
				Patterns: []string{"./..."},
			},
		},
		{
			// The JSON report carries both sides of the comparison, so a
			// consumer computes the diff from it. Rendering one too would
			// put a second, differently shaped answer on the same stream.
			name: "json suppresses diff",
			argv: []string{"--json", "--diff"},
			want: invocation{
				Mode:     modeCheck,
				Opts:     Options{JSON: true},
				Patterns: []string{"./..."},
			},
		},
		{
			name: "patterns replace the default",
			argv: []string{"./cmd/...", "./internal/..."},
			want: invocation{
				Mode:     modeCheck,
				Patterns: []string{"./cmd/...", "./internal/..."},
			},
		},
		{
			name: "a mode, a flag and a pattern together",
			argv: []string{"update", "--json=r.json", "./internal/foo/..."},
			want: invocation{
				Mode:     modeUpdate,
				Opts:     Options{JSON: true, JSONPath: "r.json"},
				Patterns: []string{"./internal/foo/..."},
			},
		},
		{
			// Help answers the whole command line, so a mode named after
			// it cannot displace it.
			name: "help outranks a mode that follows it",
			argv: []string{"--help", "update"},
			want: invocation{Mode: modeHelp},
		},
		{
			name: "the short help flag",
			argv: []string{"-h"},
			want: invocation{Mode: modeHelp},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseArgs(tc.argv)
			if err != nil {
				t.Fatalf("parseArgs(%q) returned %v", tc.argv, err)
			}
			if got.Mode != tc.want.Mode {
				t.Errorf("Mode = %q, want %q", got.Mode, tc.want.Mode)
			}
			if got.Opts != tc.want.Opts {
				t.Errorf("Opts = %+v, want %+v", got.Opts, tc.want.Opts)
			}
			if !slices.Equal(got.Patterns, tc.want.Patterns) {
				t.Errorf("Patterns = %v, want %v", got.Patterns, tc.want.Patterns)
			}
		})
	}
}

func TestParseArgs_RefusesAFlagItDoesNotKnow(t *testing.T) {
	// A go-list pattern never starts with a dash, so an unknown one is a
	// typo or a flag this build does not carry. Passing it through as a
	// pattern hands it to go list, which answers about a missing package
	// and says nothing about the flag.
	for _, argv := range [][]string{
		{"--colour"},
		{"--quite"},
		{"-x", "./..."},
		{"update", "--json=r.json", "--nope"},
	} {
		t.Run(strings.Join(argv, " "), func(t *testing.T) {
			_, err := parseArgs(argv)
			if err == nil {
				t.Fatalf("parseArgs(%q) accepted an unknown flag", argv)
			}
			if !strings.Contains(err.Error(), "unknown flag") {
				t.Errorf("error = %q, want it to name the problem", err)
			}
		})
	}
}

func TestParseArgs_TreatsAPatternWithNoDashAsAPattern(t *testing.T) {
	// The dash is the whole test, so a pattern that merely contains one
	// has to survive it.
	got, err := parseArgs([]string{"./internal/go-tools/..."})
	if err != nil {
		t.Fatalf("parseArgs returned %v", err)
	}
	if !slices.Equal(got.Patterns, []string{"./internal/go-tools/..."}) {
		t.Errorf("Patterns = %v", got.Patterns)
	}
}

// ///////////////////////////////////////////////
// RunUpdate
// ///////////////////////////////////////////////

// updateFixture builds a throwaway module holding pkgs, chdirs into it,
// seeds the allow file with allowContent, and returns a Tool whose Gather
// answers with findings.
//
// Lines are shaped "<path> <detail>", so the first field is the path scope
// resolution narrows on.
func updateFixture(t *testing.T, pkgs []string, allowContent string, findings []string) (*Tool[stringID], string) {
	t.Helper()
	dir := t.TempDir()

	write := func(rel, content string) {
		t.Helper()
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	write("go.mod", "module probe.example/x\n\ngo 1.27.0\n")
	for _, p := range pkgs {
		write(filepath.Join(p, "doc.go"), "package "+filepath.Base(p)+"\n")
	}

	allowFile := ".allow.test"
	if allowContent != "" {
		write(allowFile, allowContent)
	}
	t.Chdir(dir)

	tool := &Tool[stringID]{
		Name:       "probe",
		Title:      "Probe",
		AllowFile:  allowFile,
		UpdateCmd:  "go tool probe update",
		Categories: updateCategories,
		Gather: func([]string) []stringID {
			out := make([]stringID, len(findings))
			for i, f := range findings {
				out[i] = stringID(f)
			}
			return out
		},
		LoadAllow: func() []stringID { return nil },
		ToFinding: func(s stringID) report.Finding { return report.Finding{File: string(s)} },
		AllowPath: func(line string) string {
			path, _, _ := strings.Cut(line, " ")
			return path
		},
	}
	return tool, allowFile
}

func TestRunUpdate_KeepsTheTagOnAFindingThatIsStillThere(t *testing.T) {
	// Classifying an entry is something a maintainer does by hand. If a
	// regenerate dropped the tag, every entry would need reclassifying
	// each time, and the check would fail until somebody did it.
	tool, allowFile := updateFixture(t,
		[]string{"internal/foo"},
		"# [alpha]\ninternal/foo/x.go detail\n",
		[]string{"internal/foo/x.go detail"})

	tool.RunUpdate([]string{"./..."})

	got, err := allowlist.LoadEntries(allowFile, updateCategories)
	if err != nil {
		t.Fatalf("LoadEntries: %v", err)
	}
	want := []allowlist.Entry{{Tag: "alpha", Line: "internal/foo/x.go detail"}}
	if !slices.Equal(got, want) {
		t.Errorf("entries = %v, want %v", got, want)
	}
}

func TestRunUpdate_KeepsEntriesTheScopeDidNotLookAt(t *testing.T) {
	// A scoped run reads part of the module. Rewriting the file from that
	// read alone deletes everything else for not having been looked for,
	// and the deletion is silent and exits zero.
	tool, allowFile := updateFixture(t,
		[]string{"internal/foo", "internal/bar"},
		"# [alpha]\ninternal/foo/x.go detail\n\n# [beta]\ninternal/bar/y.go detail\n",
		[]string{"internal/foo/x.go detail"})

	tool.RunUpdate([]string{"./internal/foo/..."})

	got, err := allowlist.LoadEntries(allowFile, updateCategories)
	if err != nil {
		t.Fatalf("LoadEntries: %v", err)
	}
	want := []allowlist.Entry{
		{Tag: "alpha", Line: "internal/foo/x.go detail"},
		{Tag: "beta", Line: "internal/bar/y.go detail"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("entries = %v, want %v", got, want)
	}
}

func TestRunUpdate_LeavesANewFindingUntagged(t *testing.T) {
	// The addition nobody has classified is the one the check exists to
	// stop being waved through, so it has to arrive without a tag.
	tool, allowFile := updateFixture(t,
		[]string{"internal/foo"},
		"# [alpha]\ninternal/foo/x.go detail\n",
		[]string{"internal/foo/x.go detail", "internal/foo/new.go detail"})

	tool.RunUpdate([]string{"./..."})

	uncat, err := allowlist.Validate(allowFile, updateCategories, nil)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !slices.Equal(uncat, []string{"internal/foo/new.go detail"}) {
		t.Errorf("uncategorized = %v, want the new finding alone", uncat)
	}
}

func TestRunUpdate_WritesAFileThatDidNotExist(t *testing.T) {
	tool, allowFile := updateFixture(t,
		[]string{"internal/foo"},
		"",
		[]string{"internal/foo/x.go detail"})

	tool.RunUpdate([]string{"./..."})

	got, err := allowlist.LoadEntries(allowFile, updateCategories)
	if err != nil {
		t.Fatalf("LoadEntries: %v", err)
	}
	want := []allowlist.Entry{{Tag: "", Line: "internal/foo/x.go detail"}}
	if !slices.Equal(got, want) {
		t.Errorf("entries = %v, want %v", got, want)
	}
}

// ///////////////////////////////////////////////
// diff
// ///////////////////////////////////////////////

func TestDiff_NewAndRemoved(t *testing.T) {
	actual := []stringID{"a", "b", "c"}
	allowed := []stringID{"b", "c", "d"}
	newItems, removed := diff(actual, allowed)
	if len(newItems) != 1 || newItems[0] != "a" {
		t.Errorf("new = %v, want [a]", newItems)
	}
	if len(removed) != 1 || removed[0] != "d" {
		t.Errorf("removed = %v, want [d]", removed)
	}
}

func TestDiff_NoOverlap(t *testing.T) {
	newItems, removed := diff([]stringID{"x"}, []stringID{"y"})
	if len(newItems) != 1 || len(removed) != 1 {
		t.Errorf("new=%v, removed=%v", newItems, removed)
	}
}

func TestDiff_Empty(t *testing.T) {
	newItems, removed := diff[stringID](nil, nil)
	if len(newItems) != 0 || len(removed) != 0 {
		t.Errorf("expected empty, got new=%v removed=%v", newItems, removed)
	}
}

// ///////////////////////////////////////////////
// indexByString
// ///////////////////////////////////////////////

func TestIndexByString(t *testing.T) {
	idx := indexByString([]stringID{"a", "b", "c"})
	if len(idx) != 3 {
		t.Errorf("len = %d, want 3", len(idx))
	}
	for _, k := range []string{"a", "b", "c"} {
		if _, ok := idx[k]; !ok {
			t.Errorf("missing key %q", k)
		}
	}
}
