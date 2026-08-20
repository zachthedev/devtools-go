package allowlist

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// ///////////////////////////////////////////////
// Helpers
// ///////////////////////////////////////////////

// testCategories is the tag set the fixtures below are written against.
var testCategories = []Category{
	{Tag: "categoryA", Description: "first category"},
	{Tag: "categoryB", Description: "second category"},
}

func writeAllowFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".allow.test")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	return path
}

// ///////////////////////////////////////////////
// Validate
// ///////////////////////////////////////////////

func TestValidate_AllCategorized(t *testing.T) {
	path := writeAllowFile(t, `# Test allow list.
# [categoryA]
entry one
entry two

# [categoryB]
other entry
`)
	uncat, err := Validate(path, testCategories, nil)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(uncat) != 0 {
		t.Errorf("Validate returned %d uncategorized, want 0: %v", len(uncat), uncat)
	}
}

func TestValidate_UncategorizedAfterBlankLine(t *testing.T) {
	path := writeAllowFile(t, `# [categoryA]
entry one

floating entry
`)
	uncat, err := Validate(path, testCategories, nil)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !slices.Contains(uncat, "floating entry") {
		t.Errorf("expected 'floating entry' in uncategorized, got %v", uncat)
	}
}

func TestValidate_NoHeaderAtAll(t *testing.T) {
	path := writeAllowFile(t, `entry one
entry two
`)
	uncat, err := Validate(path, testCategories, nil)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(uncat) != 2 {
		t.Errorf("expected 2 uncategorized, got %v", uncat)
	}
}

func TestValidate_KeepFilter(t *testing.T) {
	path := writeAllowFile(t, `foo keep me
bar drop me
baz keep me too
`)
	uncat, err := Validate(path, testCategories, func(entry string) bool {
		return strings.HasPrefix(entry, "foo") || strings.HasPrefix(entry, "baz")
	})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(uncat) != 2 {
		t.Fatalf("expected 2 filtered uncategorized, got %v", uncat)
	}
}

func TestValidate_MissingFileErrors(t *testing.T) {
	_, err := Validate(filepath.Join(t.TempDir(), "nope"), testCategories, nil)
	if err == nil {
		t.Error("Validate should error on missing file")
	}
}

func TestValidate_RefusesACommentThatOnlyLooksLikeATag(t *testing.T) {
	// A tag authorizes every entry under it, so anything accepted as one
	// is a way to wave entries past review. Only a comment whose whole
	// content is a single declared tag counts.
	tests := []struct {
		name    string
		comment string
	}{
		{name: "prose that happens to bracket something", comment: "# will fix in [PR 42]"},
		{name: "empty brackets", comment: "# []"},
		{name: "brackets in the wrong order", comment: "# ]categoryA["},
		{name: "a tag with trailing prose", comment: "# [categoryA] and some notes"},
		{name: "a tag with leading prose", comment: "# see [categoryA]"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := writeAllowFile(t, tc.comment+"\nsmuggled entry\n")

			uncat, err := Validate(path, testCategories, nil)
			if err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if !slices.Contains(uncat, "smuggled entry") {
				t.Errorf("%q authorized an entry; uncategorized = %v", tc.comment, uncat)
			}
		})
	}
}

func TestValidate_RefusesATagNoCategoryDeclares(t *testing.T) {
	// An undeclared tag and a missing tag are different mistakes. One is
	// an entry nobody classified, the other is a classification the tool
	// does not offer, and reporting the second as the first sends the
	// reader looking for a tag that is already there.
	path := writeAllowFile(t, "# [totally-made-up]\nsmuggled entry\n")

	_, err := Validate(path, testCategories, nil)

	if err == nil {
		t.Fatal("Validate accepted a category no tool declares")
	}
	if !strings.Contains(err.Error(), "totally-made-up") {
		t.Errorf("error does not name the unknown tag: %v", err)
	}
}

func TestValidate_HeaderDoesNotAuthorizeTheFileWithoutItsBlankLine(t *testing.T) {
	// The generated header lists each category on its own comment line. If
	// those counted as tags, deleting the one blank line below them would
	// authorize every entry in the file, in a diff that shows nothing but
	// a removed blank line.
	path := filepath.Join(t.TempDir(), ".allow.test")
	entries := []Entry{{Line: "a b c"}, {Line: "x y z"}}
	if err := WriteUpdate(path, "Test", "go tool test update", testCategories, entries); err != nil {
		t.Fatalf("WriteUpdate: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// Drop every blank line, which is the whole of the edit.
	var kept []string
	for line := range strings.SplitSeq(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			kept = append(kept, line)
		}
	}
	if err := os.WriteFile(path, []byte(strings.Join(kept, "\n")), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	uncat, err := Validate(path, testCategories, nil)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(uncat) != 2 {
		t.Errorf("the header authorized %d of 2 entries; uncategorized = %v", 2-len(uncat), uncat)
	}
}

// ///////////////////////////////////////////////
// LoadEntries
// ///////////////////////////////////////////////

func TestLoadEntries_CarriesTheTagGoverningEachLine(t *testing.T) {
	path := writeAllowFile(t, `# Test allow list.
# [categoryA]
first
second

# [categoryB]
third

loose
`)
	got, err := LoadEntries(path, testCategories)
	if err != nil {
		t.Fatalf("LoadEntries: %v", err)
	}

	want := []Entry{
		{Tag: "categoryA", Line: "first"},
		{Tag: "categoryA", Line: "second"},
		{Tag: "categoryB", Line: "third"},
		{Tag: "", Line: "loose"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("LoadEntries = %v, want %v", got, want)
	}
}

// ///////////////////////////////////////////////
// WriteUpdate
// ///////////////////////////////////////////////

func TestWriteUpdate_ContainsHeaderAndEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".allow.test")
	cats := []Category{
		{Tag: "one", Description: "first category"},
		{Tag: "two", Description: "second category"},
	}
	entries := []Entry{{Line: "a b c"}, {Line: "x y z"}}
	if err := WriteUpdate(path, "Test", "go tool test update", cats, entries); err != nil {
		t.Fatalf("WriteUpdate: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		"# Test allow list.",
		"# Regenerate: go tool test update",
		"[one]  first category",
		"[two]  second category",
		"a b c",
		"x y z",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("WriteUpdate output missing %q:\n%s", want, got)
		}
	}
}

func TestWriteUpdate_RoundTripsTagsThroughLoadEntries(t *testing.T) {
	// Update rewrites the file it just read, so a tag that does not
	// survive the trip is a classification a maintainer has to redo.
	path := filepath.Join(t.TempDir(), ".allow.test")
	entries := []Entry{
		{Tag: "categoryA", Line: "tagged one"},
		{Tag: "categoryB", Line: "tagged two"},
		{Tag: "", Line: "untagged"},
	}
	if err := WriteUpdate(path, "Test", "go tool test update", testCategories, entries); err != nil {
		t.Fatalf("WriteUpdate: %v", err)
	}

	got, err := LoadEntries(path, testCategories)
	if err != nil {
		t.Fatalf("LoadEntries: %v", err)
	}
	if !slices.Equal(got, entries) {
		t.Errorf("round trip = %v, want %v", got, entries)
	}

	uncat, err := Validate(path, testCategories, nil)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !slices.Equal(uncat, []string{"untagged"}) {
		t.Errorf("uncategorized = %v, want [untagged]", uncat)
	}
}

// ///////////////////////////////////////////////
// LoadLines
// ///////////////////////////////////////////////

func TestLoadLines_StripsCommentsAndBlanks(t *testing.T) {
	path := writeAllowFile(t, `# header
entry one

# middle comment
entry two   # inline comment
# trailing
`)
	lines, err := LoadLines(path)
	if err != nil {
		t.Fatalf("LoadLines: %v", err)
	}
	want := []string{"entry one", "entry two"}
	if !slices.Equal(lines, want) {
		t.Errorf("LoadLines = %v, want %v", lines, want)
	}
}

func TestLoadLines_MissingFile(t *testing.T) {
	_, err := LoadLines(filepath.Join(t.TempDir(), "nope"))
	if err == nil {
		t.Error("LoadLines should error on missing file")
	}
}

// ///////////////////////////////////////////////
// IsNotExist
// ///////////////////////////////////////////////

func TestIsNotExist_SeesThroughTheWrapping(t *testing.T) {
	// The errors here are wrapped with %w, and os.IsNotExist does not
	// unwrap. A caller reaching for that helper gets false for a file that
	// is plainly absent, and the missing-file branch it guards never runs.
	missing := filepath.Join(t.TempDir(), "nope")

	_, err := LoadLines(missing)
	if err == nil {
		t.Fatal("LoadLines accepted a missing file")
	}
	if !IsNotExist(err) {
		t.Errorf("IsNotExist(%v) = false, want true", err)
	}
	if os.IsNotExist(err) {
		t.Error("os.IsNotExist now unwraps; this helper can go away")
	}

	_, err = LoadEntries(missing, testCategories)
	if err == nil {
		t.Fatal("LoadEntries accepted a missing file")
	}
	if !IsNotExist(err) {
		t.Errorf("IsNotExist(%v) = false, want true", err)
	}
}

func TestIsNotExist_IsFalseForOtherFailures(t *testing.T) {
	path := writeAllowFile(t, "# [totally-made-up]\nentry\n")

	_, err := LoadEntries(path, testCategories)
	if err == nil {
		t.Fatal("LoadEntries accepted an unknown category")
	}
	if IsNotExist(err) {
		t.Errorf("IsNotExist reported true for %v", err)
	}
}
