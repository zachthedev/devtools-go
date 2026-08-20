package scope

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
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

func TestMatcher_EmptyPkgDirsMatchesNothing(t *testing.T) {
	// No resolved package means no scope was established, and a matcher
	// that said yes there would hand a caller a scope it never had. The
	// direction matters: a yes keeps every allow entry while the scan
	// found none, and the check then reports a clean pass over nothing.
	// A no leaves every finding unallowed, which fails loudly.
	m := Matcher(nil)
	for _, c := range []string{"foo", "foo/bar.go", "internal/x/y.go", ""} {
		if m(c) {
			t.Errorf("Matcher(nil) accepted %q", c)
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
	// A backslash path has to match a forward-slash scope on every
	// platform, not just on Windows. Allow files are committed, so the
	// runner reading one is rarely the machine that wrote it, and
	// filepath.ToSlash would answer differently on each.
	m := Matcher([]string{"internal/foo"})
	if !m(`internal\foo\bar.go`) {
		t.Error("Matcher should normalize backslashes")
	}
}

// ///////////////////////////////////////////////
// PackageDirs
// ///////////////////////////////////////////////

// inModule runs fn with the working directory set to a throwaway module
// holding the given package directories.
func inModule(t *testing.T, pkgs []string, fn func()) {
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
	// An empty directory that go list will match no package in.
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}

	// t.Chdir restores on cleanup and refuses a parallel test, which a
	// process-wide working directory cannot survive.
	t.Chdir(dir)
	fn()
}

func TestPackageDirs_ResolvesPatternsToRelativeDirs(t *testing.T) {
	inModule(t, []string{"internal/foo", "internal/bar"}, func() {
		got, err := PackageDirs([]string{"./internal/..."})
		if err != nil {
			t.Fatalf("PackageDirs: %v", err)
		}
		want := []string{"internal/bar", "internal/foo"}
		if !slices.Equal(got, want) {
			t.Errorf("PackageDirs = %v, want %v", got, want)
		}
	})
}

func TestPackageDirs_RefusesAPatternThatMatchesNoPackage(t *testing.T) {
	// go list reports this as a warning and still exits zero. Reading only
	// its combined output would narrow the scope to whatever did match, and
	// a scope narrowed to nothing finds nothing, which a check calls a pass.
	inModule(t, []string{"internal/foo"}, func() {
		_, err := PackageDirs([]string{"./docs/..."})

		if err == nil {
			t.Fatal("PackageDirs accepted a pattern matching no package")
		}
		if !strings.Contains(err.Error(), "./docs/...") {
			t.Errorf("error does not name the pattern: %v", err)
		}
	})
}

func TestPackageDirs_RefusesADeadPatternBesideALiveOne(t *testing.T) {
	// The half-vacuous case, and the one that gives no signal at all. One
	// good pattern still produces directories, so a caller checking only
	// for an empty result would carry on with a scope quietly missing
	// whatever the dead pattern was meant to cover.
	inModule(t, []string{"internal/foo"}, func() {
		_, err := PackageDirs([]string{"./internal/...", "./docs/..."})

		if err == nil {
			t.Fatal("PackageDirs accepted a dead pattern beside a live one")
		}
		if !strings.Contains(err.Error(), "./docs/...") {
			t.Errorf("error does not name the dead pattern: %v", err)
		}
	})
}

func TestPackageDirs_DefaultsToTheWholeModule(t *testing.T) {
	inModule(t, []string{"internal/foo"}, func() {
		got, err := PackageDirs(nil)
		if err != nil {
			t.Fatalf("PackageDirs: %v", err)
		}
		if !slices.Contains(got, "internal/foo") {
			t.Errorf("PackageDirs(nil) = %v, want it to include internal/foo", got)
		}
	})
}

func TestPackageDirs_ReportsAPatternGoListRejects(t *testing.T) {
	inModule(t, []string{"internal/foo"}, func() {
		if _, err := PackageDirs([]string{"./nope/..."}); err == nil {
			t.Error("PackageDirs accepted a directory that does not exist")
		}
	})
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
