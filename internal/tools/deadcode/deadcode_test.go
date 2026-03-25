package deadcode

import "testing"

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
