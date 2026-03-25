package scope

import (
	"fmt"
	"testing"
)

// stringID is a fmt.Stringer used by Filter's tests.
type stringID string

// verify stringID satisfies fmt.Stringer at compile time.
var _ fmt.Stringer = stringID("")

func (s stringID) String() string { return string(s) }

// ///////////////////////////////////////////////
// Matcher
// ///////////////////////////////////////////////

func TestMatcher_EmptyPkgDirsMatchesEverything(t *testing.T) {
	m := Matcher(nil)
	cases := []string{"foo", "foo/bar.go", "internal/x/y.go", ""}
	for _, c := range cases {
		if !m(c) {
			t.Errorf("Matcher(nil) rejected %q", c)
		}
	}
}

func TestMatcher_RootDotMatchesEverything(t *testing.T) {
	m := Matcher([]string{"."})
	if !m("internal/foo/bar.go") {
		t.Error("Matcher with . should match any file")
	}
}

func TestMatcher_ScopedPaths(t *testing.T) {
	m := Matcher([]string{"internal/foo", "cmd/bar"})
	tests := []struct {
		entry string
		want  bool
	}{
		{"internal/foo/x.go", true},
		{"internal/foo", true},
		{"internal/foobar/x.go", false},
		{"cmd/bar/main.go", true},
		{"cmd/baz/main.go", false},
		{"other/file.go", false},
	}
	for _, tt := range tests {
		t.Run(tt.entry, func(t *testing.T) {
			if got := m(tt.entry); got != tt.want {
				t.Errorf("m(%q) = %v, want %v", tt.entry, got, tt.want)
			}
		})
	}
}

func TestMatcher_ExtractsFilePathFromEntryLine(t *testing.T) {
	m := Matcher([]string{"internal/foo"})
	// Allow-list line form: "kind file message"
	if !m("missing-test internal/foo/bar.go expected internal/foo/bar_test.go") {
		t.Error("Matcher should extract file from 'kind file message' triple")
	}
	if m("missing-test internal/other/bar.go expected internal/other/bar_test.go") {
		t.Error("Matcher should reject file outside scope")
	}
}

func TestMatcher_ForwardSlashNormalization(t *testing.T) {
	// Windows-style backslash paths should match against forward-slash scopes.
	m := Matcher([]string{"internal/foo"})
	if !m("internal\\foo\\bar.go") {
		t.Error("Matcher should normalize backslashes")
	}
}

// ///////////////////////////////////////////////
// Filter
// ///////////////////////////////////////////////

func TestFilter_NilPredicatePassthrough(t *testing.T) {
	items := []stringID{"a", "b"}
	got := Filter(items, nil)
	if len(got) != 2 {
		t.Errorf("Filter(nil) dropped items: got %d, want 2", len(got))
	}
}

func TestFilter_PredicateFilters(t *testing.T) {
	items := []stringID{"keep-1", "drop", "keep-2"}
	got := Filter(items, func(s string) bool { return len(s) > 4 })
	if len(got) != 2 {
		t.Fatalf("Filter len = %d, want 2", len(got))
	}
	if got[0] != "keep-1" || got[1] != "keep-2" {
		t.Errorf("Filter = %v, want [keep-1, keep-2]", got)
	}
}

func TestFilter_EmptyInput(t *testing.T) {
	got := Filter[stringID](nil, func(string) bool { return true })
	if len(got) != 0 {
		t.Errorf("Filter(nil, _) len = %d, want 0", len(got))
	}
}
