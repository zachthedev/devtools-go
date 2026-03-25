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
	uncat, err := Validate(path, nil)
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
	uncat, err := Validate(path, nil)
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
	uncat, err := Validate(path, nil)
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
	uncat, err := Validate(path, func(entry string) bool {
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
	_, err := Validate(filepath.Join(t.TempDir(), "nope"), nil)
	if err == nil {
		t.Error("Validate should error on missing file")
	}
}

// ///////////////////////////////////////////////
// WriteUpdate + LoadLines round-trip
// ///////////////////////////////////////////////

func TestWriteUpdate_ContainsHeaderAndEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".allow.test")
	cats := []Category{
		{Tag: "one", Description: "first category"},
		{Tag: "two", Description: "second category"},
	}
	entries := []string{"a b c", "x y z"}
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
