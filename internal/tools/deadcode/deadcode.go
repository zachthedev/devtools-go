// Package deadcode implements the deadcode devtool. Wraps the upstream
// deadcode binary (golang.org/x/tools/cmd/deadcode) and compares its
// output against .allow.deadcode.
//
// Findings are compared against the allow list. See the driver package
// for the check/update/validate/--json lifecycle.
package deadcode

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"

	"zach.tools/go/devtools/internal/allowlist"
	"zach.tools/go/devtools/internal/driver"
	"zach.tools/go/devtools/internal/report"
)

// ///////////////////////////////////////////////
// Types
// ///////////////////////////////////////////////

// Entry is a normalized deadcode finding: file path and function name.
type Entry struct {
	File string `json:"file"`
	Func string `json:"func"`
}

// ///////////////////////////////////////////////
// Constants
// ///////////////////////////////////////////////

const allowFile = ".allow.deadcode"

var deadcodePattern = regexp.MustCompile(`^([^ ]+):\d+:\d+: unreachable func: (.+)$`)

// ///////////////////////////////////////////////
// Methods
// ///////////////////////////////////////////////

// String returns the canonical form used in .allow.deadcode.
func (e Entry) String() string { return e.File + " " + e.Func }

// ///////////////////////////////////////////////
// Tool
// ///////////////////////////////////////////////

// Tool returns a configured [driver.Tool] for the deadcode subcommand.
// Call [driver.Main] on the result from a main package or dispatcher.
func Tool() *driver.Tool[Entry] {
	return &driver.Tool[Entry]{
		Name:             "deadcode",
		Title:            "Deadcode",
		AllowFile:        allowFile,
		UpdateCmd:        "go tool devtools deadcode update",
		RequireAllowFile: true,
		Categories: []allowlist.Category{
			{Tag: "public-api", Description: "exported API not yet called from cmd/"},
			{Tag: "test-only", Description: "called from _test.go files; deadcode can't trace test imports"},
			{Tag: "platform", Description: "platform-specific code not built on current OS"},
			{Tag: "scaffold", Description: "framework wiring for future use"},
		},
		Gather:    invokeDeadcode,
		LoadAllow: loadAllowed,
		ToFinding: func(e Entry) report.Finding {
			return report.Finding{Kind: "unreachable", File: e.File, Detail: e.Func}
		},
	}
}

// ///////////////////////////////////////////////
// Deadcode Invocation
// ///////////////////////////////////////////////

// invokeDeadcode runs the deadcode binary against the given patterns and
// returns normalized, sorted entries.
func invokeDeadcode(patterns []string) []Entry {
	cmd := exec.Command("deadcode", slices.Clone(patterns)...) //nolint:gosec // args are go list patterns
	output, err := cmd.CombinedOutput()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			fmt.Fprintln(os.Stderr, "error: deadcode not installed; run go install golang.org/x/tools/cmd/deadcode@latest")
			os.Exit(2)
		}
		// deadcode exits non-zero when it finds dead code; that's expected.
		// Only fail if there's no output to parse (actual invocation failure).
		if len(output) == 0 {
			fmt.Fprintf(os.Stderr, "error: deadcode failed: %v\n", err)
			os.Exit(2)
		}
	}
	return parseOutput(string(output))
}

// parseOutput extracts file/func pairs from raw deadcode output.
func parseOutput(output string) []Entry {
	var entries []Entry
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		line = strings.ReplaceAll(line, "\\", "/")
		m := deadcodePattern.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		entries = append(entries, Entry{File: m[1], Func: m[2]})
	}
	slices.SortFunc(entries, func(a, b Entry) int { return cmp.Compare(a.String(), b.String()) })
	return entries
}

// ///////////////////////////////////////////////
// Allow List
// ///////////////////////////////////////////////

// loadAllowed reads .allow.deadcode entries.
func loadAllowed() []Entry {
	lines, err := allowlist.LoadLines(allowFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
	var entries []Entry
	for _, line := range lines {
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			continue
		}
		entries = append(entries, Entry{File: parts[0], Func: parts[1]})
	}
	slices.SortFunc(entries, func(a, b Entry) int { return cmp.Compare(a.String(), b.String()) })
	return entries
}
