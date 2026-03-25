package driver

import (
	"os"
	"slices"
	"testing"
)

// ///////////////////////////////////////////////
// Helpers
// ///////////////////////////////////////////////

// stringID is a fmt.Stringer for exercising generic helpers.
type stringID string

func (s stringID) String() string { return string(s) }

// withArgs temporarily replaces os.Args for a test and restores on cleanup.
func withArgs(t *testing.T, args ...string) {
	t.Helper()
	orig := os.Args
	os.Args = append([]string{"tool"}, args...)
	t.Cleanup(func() { os.Args = orig })
}

// ///////////////////////////////////////////////
// parseArgs
// ///////////////////////////////////////////////

func TestParseArgs_Defaults(t *testing.T) {
	withArgs(t)
	mode, opts, patterns := parseArgs("tool")
	if mode != "check" {
		t.Errorf("mode = %q, want check", mode)
	}
	if opts.JSON || opts.Quiet || opts.Diff {
		t.Errorf("opts = %+v, want all false", opts)
	}
	if !slices.Equal(patterns, []string{"./..."}) {
		t.Errorf("patterns = %v, want [./...]", patterns)
	}
}

func TestParseArgs_UpdateMode(t *testing.T) {
	withArgs(t, "update")
	mode, _, _ := parseArgs("tool")
	if mode != "update" {
		t.Errorf("mode = %q, want update", mode)
	}
}

func TestParseArgs_ValidateMode(t *testing.T) {
	withArgs(t, "validate")
	mode, _, _ := parseArgs("tool")
	if mode != "validate" {
		t.Errorf("mode = %q, want validate", mode)
	}
}

func TestParseArgs_JSONFlag(t *testing.T) {
	withArgs(t, "--json")
	_, opts, _ := parseArgs("tool")
	if !opts.JSON || opts.JSONPath != "" {
		t.Errorf("--json: opts = %+v, want JSON=true, no path", opts)
	}
}

func TestParseArgs_JSONWithPath(t *testing.T) {
	withArgs(t, "--json=out.json")
	_, opts, _ := parseArgs("tool")
	if !opts.JSON || opts.JSONPath != "out.json" {
		t.Errorf("--json=out.json: opts = %+v", opts)
	}
}

func TestParseArgs_QuietAndDiff(t *testing.T) {
	withArgs(t, "--quiet", "--diff")
	_, opts, _ := parseArgs("tool")
	if !opts.Quiet || !opts.Diff {
		t.Errorf("expected both flags set, got %+v", opts)
	}
}

func TestParseArgs_JSONSuppressesDiff(t *testing.T) {
	withArgs(t, "--json", "--diff")
	_, opts, _ := parseArgs("tool")
	if !opts.JSON {
		t.Error("JSON flag lost")
	}
	if opts.Diff {
		t.Error("Diff should be suppressed when JSON is set")
	}
}

func TestParseArgs_Patterns(t *testing.T) {
	withArgs(t, "./cmd/...", "./internal/...")
	_, _, patterns := parseArgs("tool")
	want := []string{"./cmd/...", "./internal/..."}
	if !slices.Equal(patterns, want) {
		t.Errorf("patterns = %v, want %v", patterns, want)
	}
}

func TestParseArgs_MixedFlagsAndPatterns(t *testing.T) {
	withArgs(t, "update", "--json=r.json", "./internal/foo/...")
	mode, opts, patterns := parseArgs("tool")
	if mode != "update" {
		t.Errorf("mode = %q, want update", mode)
	}
	if !opts.JSON || opts.JSONPath != "r.json" {
		t.Errorf("opts = %+v", opts)
	}
	if !slices.Equal(patterns, []string{"./internal/foo/..."}) {
		t.Errorf("patterns = %v", patterns)
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
