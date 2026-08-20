package deadcode

import (
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
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
	got, unparsed := parseOutput(raw)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if len(unparsed) != 0 {
		t.Errorf("unparsed = %q, want none", unparsed)
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
	got, _ := parseOutput(raw)
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

func TestParseOutput_ReportsALineItCannotMatch(t *testing.T) {
	// Only findings reach the analyzer's stdout, so an unmatched line means
	// the output format moved. Returning it is what lets the caller refuse
	// the run. Dropping it would shrink the finding set with no sign, which
	// is how a check passes on the strength of what it failed to read.
	raw := `some header
internal/foo/bar.go:12:6: unreachable func: Bar
another warning
`
	got, unparsed := parseOutput(raw)

	if len(got) != 1 {
		t.Errorf("len(entries) = %d, want 1", len(got))
	}
	want := []string{"some header", "another warning"}
	if len(unparsed) != len(want) {
		t.Fatalf("unparsed = %q, want %q", unparsed, want)
	}
	for i, line := range want {
		if unparsed[i] != line {
			t.Errorf("unparsed[%d] = %q, want %q", i, unparsed[i], line)
		}
	}
}

func TestParseOutput_ReportsEveryLineWhenTheAnalyzerEmitsNoFindings(t *testing.T) {
	// The exact shape of the failure this reporting exists to catch: a
	// stale analyzer answers with diagnostics only. Zero findings and zero
	// complaints would read as a repository with no dead code.
	raw := `internal/buildenv/buildenv.go:23:1: package requires newer Go version go1.27 (application built with go1.26)
internal/paths/paths.go:14:1: package requires newer Go version go1.27 (application built with go1.26)
`
	got, unparsed := parseOutput(raw)

	if len(got) != 0 {
		t.Errorf("len(entries) = %d, want 0", len(got))
	}
	if len(unparsed) != 2 {
		t.Errorf("len(unparsed) = %d, want 2; a silent drop here empties the gate", len(unparsed))
	}
}

func TestParseOutput_NormalizesBackslashPaths(t *testing.T) {
	raw := `internal\foo\bar.go:12:6: unreachable func: Bar
`
	got, _ := parseOutput(raw)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].File != "internal/foo/bar.go" {
		t.Errorf("path not normalized: got %q", got[0].File)
	}
}

func TestParseOutput_EmptyInput(t *testing.T) {
	got, unparsed := parseOutput("")
	if len(got) != 0 {
		t.Errorf("empty input produced %d entries", len(got))
	}
	if len(unparsed) != 0 {
		t.Errorf("empty input produced %d unparsed lines", len(unparsed))
	}
}

func TestParseOutput_TreatsBlankLinesAsNeitherFindingNorNoise(t *testing.T) {
	raw := "\n\ninternal/foo/bar.go:1:1: unreachable func: Bar\n\n"
	got, unparsed := parseOutput(raw)
	if len(got) != 1 {
		t.Errorf("len(entries) = %d, want 1", len(got))
	}
	if len(unparsed) != 0 {
		t.Errorf("blank lines reported as unparsed: %q", unparsed)
	}
}

// ///////////////////////////////////////////////
// diagnose
// ///////////////////////////////////////////////

func TestDiagnose_NamesTheCauseAndTheFix(t *testing.T) {
	// The stderr below is what the analyzer really writes in each case, so
	// the assertions describe the message a user actually has to act on.
	const skew = `internal/buildenv/buildenv.go:23:1: package requires newer Go version go1.27 (application built with go1.26)
2026/08/20 09:14:02 packages contain errors`

	tests := []struct {
		name   string
		err    error
		stderr string
		want   []string
		absent []string
	}{
		{
			name:   "missing binary names the install command",
			err:    fmt.Errorf("exec deadcode: %w", exec.ErrNotFound),
			stderr: "",
			want:   []string{"deadcode", "not installed", installCmd},
		},
		{
			name:   "version skew names both versions and the rebuild",
			err:    errors.New("exit status 1"),
			stderr: skew,
			want:   []string{"built with go1.26", "requiring go1.27", installCmd},
			// A bare exit status explains nothing on its own, so the skew
			// message has to replace it rather than sit beside it.
			absent: []string{"exit status 1"},
		},
		{
			name:   "an unrecognized failure still surfaces the error",
			err:    errors.New("signal: killed"),
			stderr: "some unfamiliar complaint",
			want:   []string{"deadcode failed", "signal: killed"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := diagnose("deadcode", tc.err, tc.stderr)
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("diagnose() = %q, missing %q", got, want)
				}
			}
			for _, absent := range tc.absent {
				if strings.Contains(got, absent) {
					t.Errorf("diagnose() = %q, should not contain %q", got, absent)
				}
			}
		})
	}
}

func TestDiagnose_UsesTheBinaryNameItWasGiven(t *testing.T) {
	// The binary is overridable, so a message naming a hardcoded "deadcode"
	// would send the reader to a build that is not the one that failed.
	got := diagnose("/opt/pinned/deadcode", errors.New("exit status 1"), "")
	if !strings.Contains(got, "/opt/pinned/deadcode") {
		t.Errorf("diagnose() = %q, want it to name the binary that ran", got)
	}
}

// ///////////////////////////////////////////////
// resolveAnalyzer
// ///////////////////////////////////////////////

func TestResolveAnalyzer_PrefersABuildTheModuleOwns(t *testing.T) {
	// A module-owned build is compiled by whatever toolchain is running,
	// so it cannot fall behind the code it reads. A PATH binary can, and
	// that drift is the whole failure this ordering exists to avoid.
	tests := []struct {
		name         string
		override     string
		moduleBuilds bool
		wantName     string
		wantArgs     []string
		wantLabel    string
	}{
		{
			name:         "module-owned build beats PATH",
			moduleBuilds: true,
			wantName:     "go",
			wantArgs:     []string{"tool", toolPath},
			wantLabel:    "go tool " + toolPath,
		},
		{
			name:         "PATH is the fallback when the module declares nothing",
			moduleBuilds: false,
			wantName:     defaultBin,
			wantArgs:     nil,
			wantLabel:    defaultBin,
		},
		{
			name:         "an override outranks a module-owned build",
			override:     "/opt/pinned/deadcode",
			moduleBuilds: true,
			wantName:     "/opt/pinned/deadcode",
			wantArgs:     nil,
			wantLabel:    "/opt/pinned/deadcode",
		},
		{
			name:         "an override outranks PATH",
			override:     "/opt/pinned/deadcode",
			moduleBuilds: false,
			wantName:     "/opt/pinned/deadcode",
			wantArgs:     nil,
			wantLabel:    "/opt/pinned/deadcode",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			name, args, label := resolveAnalyzer(tc.override, func() bool { return tc.moduleBuilds })

			if name != tc.wantName {
				t.Errorf("name = %q, want %q", name, tc.wantName)
			}
			if !slices.Equal(args, tc.wantArgs) {
				t.Errorf("args = %q, want %q", args, tc.wantArgs)
			}
			if label != tc.wantLabel {
				t.Errorf("label = %q, want %q", label, tc.wantLabel)
			}
		})
	}
}

func TestResolveAnalyzer_DoesNotProbeTheModuleWhenOverridden(t *testing.T) {
	// The probe shells out to go, and an override already settles the
	// question. Running it anyway would spend a subprocess on an answer
	// nothing reads.
	probed := false
	resolveAnalyzer("/opt/pinned/deadcode", func() bool { probed = true; return true })

	if probed {
		t.Error("resolveAnalyzer probed the module despite an override")
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
