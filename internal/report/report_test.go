package report

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fatih/color"
)

// TestMain disables colored output so tests assert on plain text,
// regardless of terminal or environment detection.
func TestMain(m *testing.M) {
	color.NoColor = true
	os.Exit(m.Run())
}

// ///////////////////////////////////////////////
// Count
// ///////////////////////////////////////////////

func TestCount(t *testing.T) {
	tests := []struct {
		name     string
		n        int
		singular string
		plural   string
		want     string
	}{
		{"zero regular", 0, "finding", "", "0 findings"},
		{"one regular", 1, "finding", "", "1 finding"},
		{"many regular", 3, "finding", "", "3 findings"},
		{"one irregular", 1, "entry", "entries", "1 entry"},
		{"many irregular", 7, "entry", "entries", "7 entries"},
		{"zero irregular", 0, "entry", "entries", "0 entries"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Count(tt.n, tt.singular, tt.plural); got != tt.want {
				t.Errorf("Count(%d, %q, %q) = %q, want %q", tt.n, tt.singular, tt.plural, got, tt.want)
			}
		})
	}
}

// ///////////////////////////////////////////////
// Block printers
// ///////////////////////////////////////////////

func TestPrintCleanPass(t *testing.T) {
	var buf bytes.Buffer
	PrintCleanPass(&buf, 12, ".allow.testpair")
	got := buf.String()
	for _, want := range []string{"OK", "0 new findings", "12 known exceptions", ".allow.testpair"} {
		if !strings.Contains(got, want) {
			t.Errorf("PrintCleanPass output = %q, want to contain %q", got, want)
		}
	}
}

func TestPrintCleanPass_OneKnownException(t *testing.T) {
	var buf bytes.Buffer
	PrintCleanPass(&buf, 1, ".allow.x")
	if !strings.Contains(buf.String(), "1 known exception") {
		t.Errorf("PrintCleanPass should pluralize singular; got %q", buf.String())
	}
}

func TestPrintFindings(t *testing.T) {
	var buf bytes.Buffer
	PrintFindings(&buf, []Finding{
		{Kind: "missing-test", File: "foo/bar.go", Detail: "expected foo/bar_test.go"},
		{Kind: "orphan-test", File: "foo/baz_test.go", Detail: "no source"},
	}, ".allow.testpair")
	got := buf.String()
	for _, want := range []string{
		"FAIL:",
		"2 new findings",
		".allow.testpair",
		"[missing-test]",
		"foo/bar.go",
		"expected foo/bar_test.go",
		"[orphan-test]",
		"Fix the finding",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("PrintFindings output missing %q:\n%s", want, got)
		}
	}
}

func TestPrintRemoved(t *testing.T) {
	var buf bytes.Buffer
	PrintRemoved(&buf, []Finding{
		{Kind: "missing-test", File: "a.go", Detail: "x"},
	}, ".allow.testpair", "go tool devtools testpair update")
	got := buf.String()
	for _, want := range []string{
		"1 allow-list entry",
		"is no longer",
		".allow.testpair",
		"a.go",
		"go tool devtools testpair update",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("PrintRemoved output missing %q:\n%s", want, got)
		}
	}
}

func TestPrintRemoved_PluralAgreement(t *testing.T) {
	var buf bytes.Buffer
	PrintRemoved(&buf, []Finding{
		{Kind: "k", File: "a.go", Detail: "x"},
		{Kind: "k", File: "b.go", Detail: "y"},
	}, ".allow.x", "update")
	got := buf.String()
	if !strings.Contains(got, "2 allow-list entries") {
		t.Errorf("expected plural subject: %q", got)
	}
	if !strings.Contains(got, "are no longer") {
		t.Errorf("expected plural verb: %q", got)
	}
}

func TestPrintUpdated(t *testing.T) {
	var buf bytes.Buffer
	PrintUpdated(&buf, 5, ".allow.deadcode")
	got := buf.String()
	for _, want := range []string{"Updated", ".allow.deadcode", "5 entries"} {
		if !strings.Contains(got, want) {
			t.Errorf("PrintUpdated output missing %q: %q", want, got)
		}
	}
}

func TestPrintUncategorized(t *testing.T) {
	var buf bytes.Buffer
	PrintUncategorized(&buf, []string{"missing-test foo.go x", "orphan-test bar_test.go y"}, ".allow.x")
	got := buf.String()
	for _, want := range []string{
		"FAIL:",
		"2 allow-list entries",
		".allow.x",
		"[category]",
		"missing-test foo.go x",
		"orphan-test bar_test.go y",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("PrintUncategorized output missing %q:\n%s", want, got)
		}
	}
}

func TestPrintDiff(t *testing.T) {
	var buf bytes.Buffer
	PrintDiff(&buf,
		[]Finding{{Kind: "missing-test", File: "new.go", Detail: "d"}},
		[]Finding{{Kind: "orphan-test", File: "old_test.go", Detail: "d"}},
		".allow.x", "update")
	got := buf.String()
	for _, want := range []string{"diff", "1 added", "1 removed", "+", "-", "new.go", "old_test.go"} {
		if !strings.Contains(got, want) {
			t.Errorf("PrintDiff output missing %q:\n%s", want, got)
		}
	}
}

func TestPrintDiff_OnlyAdditions(t *testing.T) {
	var buf bytes.Buffer
	PrintDiff(&buf,
		[]Finding{{Kind: "k", File: "a.go", Detail: "d"}},
		nil,
		".allow.x", "update")
	if !strings.Contains(buf.String(), "Fix the additions") {
		t.Errorf("additions-only footer missing: %q", buf.String())
	}
}

// ///////////////////////////////////////////////
// JSON output
// ///////////////////////////////////////////////

func TestWriteJSONTo(t *testing.T) {
	var buf bytes.Buffer
	WriteJSONTo(&buf, map[string]any{"foo": "bar", "count": 3})
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["foo"] != "bar" {
		t.Errorf("foo = %v, want %q", got["foo"], "bar")
	}
	if got["count"].(float64) != 3 {
		t.Errorf("count = %v, want 3", got["count"])
	}
}

func TestCoalesce(t *testing.T) {
	t.Run("nil becomes empty", func(t *testing.T) {
		var s []int
		got := Coalesce(s)
		if got == nil {
			t.Error("Coalesce(nil) returned nil, want empty slice")
		}
		if len(got) != 0 {
			t.Errorf("Coalesce(nil) len = %d, want 0", len(got))
		}
	})
	t.Run("passthrough", func(t *testing.T) {
		in := []string{"a", "b"}
		got := Coalesce(in)
		if len(got) != 2 {
			t.Errorf("Coalesce passthrough len = %d, want 2", len(got))
		}
	})
}

// ///////////////////////////////////////////////
// OpenJSONOutput
// ///////////////////////////////////////////////

func TestOpenJSONOutput_StdoutWhenEmpty(t *testing.T) {
	w, closer := OpenJSONOutput("")
	defer closer()
	if w != os.Stdout {
		t.Error("OpenJSONOutput(\"\") did not return os.Stdout")
	}
}

func TestOpenJSONOutput_FileWhenPathGiven(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.json")
	w, closer := OpenJSONOutput(path)
	if _, err := w.Write([]byte("{}\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	closer()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "{}\n" {
		t.Errorf("file content = %q, want %q", got, "{}\n")
	}
}
