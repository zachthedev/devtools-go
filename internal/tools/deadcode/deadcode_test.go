package deadcode

import (
	"testing"

	"zach.tools/go/devtools/internal/scope"
)

// ///////////////////////////////////////////////
// Entry.String
// ///////////////////////////////////////////////

func TestEntry_String(t *testing.T) {
	e := Entry{File: "internal/foo/bar.go", Func: "SomeFunc"}
	if got, want := e.String(), "internal/foo/bar.go SomeFunc"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

// ///////////////////////////////////////////////
// parseOutput
// ///////////////////////////////////////////////

func TestParseOutput_SingleEntry(t *testing.T) {
	raw := "internal/foo/bar.go:12:6: unreachable func: SomeFunc\n"
	got := parseOutput(raw)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].File != "internal/foo/bar.go" || got[0].Func != "SomeFunc" {
		t.Errorf("got = %+v", got[0])
	}
}

func TestParseOutput_MultipleEntriesSorted(t *testing.T) {
	raw := `internal/z/z.go:1:1: unreachable func: ZFunc
internal/a/a.go:1:1: unreachable func: AFunc
internal/m/m.go:1:1: unreachable func: MFunc
`
	got := parseOutput(raw)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	// sorted by String() = File + " " + Func
	want := []string{"internal/a/a.go", "internal/m/m.go", "internal/z/z.go"}
	for i, e := range got {
		if e.File != want[i] {
			t.Errorf("got[%d].File = %q, want %q", i, e.File, want[i])
		}
	}
}

func TestParseOutput_IgnoresNonMatchingLines(t *testing.T) {
	raw := `some header
internal/foo/bar.go:12:6: unreachable func: Bar
another warning
`
	got := parseOutput(raw)
	if len(got) != 1 {
		t.Errorf("len = %d, want 1 (non-matching lines should be skipped)", len(got))
	}
}

func TestParseOutput_NormalizesBackslashPaths(t *testing.T) {
	raw := `internal\foo\bar.go:12:6: unreachable func: Bar
`
	got := parseOutput(raw)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].File != "internal/foo/bar.go" {
		t.Errorf("path not normalized: got %q", got[0].File)
	}
}

func TestParseOutput_EmptyInput(t *testing.T) {
	got := parseOutput("")
	if len(got) != 0 {
		t.Errorf("empty input produced %d entries", len(got))
	}
}

// ///////////////////////////////////////////////
// allowPath
// ///////////////////////////////////////////////

func TestAllowPath_ResolvesAnEntryToAPathScopeMatchingAccepts(t *testing.T) {
	// The round trip the accessor exists for. An entry's own canonical line
	// has to resolve to a path a scope matcher accepts, or every entry
	// filters out of scope and the tool rejects the allow file it wrote.
	entry := Entry{File: "internal/foo/bar.go", Func: "SomeFunc"}

	if got, want := allowPath(entry.String()), "internal/foo/bar.go"; got != want {
		t.Errorf("allowPath(%q) = %q, want %q", entry.String(), got, want)
	}
	if !scope.Matcher([]string{"internal/foo"})(allowPath(entry.String())) {
		t.Error("a scoped run drops an entry that is inside its own scope")
	}
}

func TestAllowPath_AnswersWithWhatALineWithNoFunctionHolds(t *testing.T) {
	// A hand-edited allow file is ordinary, and a line missing its second
	// field still names a path. Answering empty would match every scope.
	if got, want := allowPath("internal/foo/bar.go"), "internal/foo/bar.go"; got != want {
		t.Errorf("allowPath = %q, want %q", got, want)
	}
}
