package scope

import (
	"fmt"
	"strings"
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

func TestMatcher_JudgesThePathItIsGivenAndParsesNothing(t *testing.T) {
	// Matcher judges a path, and taking a line apart is the caller's job.
	// The two tools hold their paths in different fields, so a matcher that
	// dug one out itself would answer no for whichever shape it did not
	// expect. A wrong no reads as an entry out of scope rather than as a
	// line nothing could parse, which is how an allow list goes quietly
	// unread.
	m := Matcher([]string{"internal/foo"})

	if !m("internal/foo/bar.go") {
		t.Error("Matcher rejected a path inside the scope")
	}
	if m("missing-test internal/foo/bar.go expected internal/foo/bar_test.go") {
		t.Error("Matcher accepted a whole allow-file line, want it to judge paths only")
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
	got := Filter(items, stringID.String, nil)
	if len(got) != 2 {
		t.Errorf("Filter(nil) dropped items: got %d, want 2", len(got))
	}
}

func TestFilter_PredicateFilters(t *testing.T) {
	items := []stringID{"keep-1", "drop", "keep-2"}
	got := Filter(items, stringID.String, func(s string) bool { return len(s) > 4 })
	if len(got) != 2 {
		t.Fatalf("Filter len = %d, want 2", len(got))
	}
	if got[0] != "keep-1" || got[1] != "keep-2" {
		t.Errorf("Filter = %v, want [keep-1, keep-2]", got)
	}
}

func TestFilter_EmptyInput(t *testing.T) {
	got := Filter[stringID](nil, stringID.String, func(string) bool { return true })
	if len(got) != 0 {
		t.Errorf("Filter(nil, _) len = %d, want 0", len(got))
	}
}

func TestFilter_AsksTheAccessorRatherThanTheItemsText(t *testing.T) {
	// The accessor is what decouples scope resolution from an allow file's
	// line format. A Filter that read the item's own text instead would put
	// the format back in the matcher's way.
	items := []stringID{"unreachable internal/foo/a.go Fn", "unreachable internal/bar/b.go Fn"}
	paths := func(s stringID) string {
		fields := strings.Fields(string(s))
		return fields[1]
	}

	got := Filter(items, paths, Matcher([]string{"internal/foo"}))
	if len(got) != 1 {
		t.Fatalf("Filter len = %d, want 1", len(got))
	}
	if got[0] != items[0] {
		t.Errorf("Filter kept %q, want %q", got[0], items[0])
	}
}
