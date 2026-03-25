// Deadcode enforcement with allow-list management.
//
// Wraps the deadcode tool (golang.org/x/tools/cmd/deadcode) and compares
// output against .deadcode-allow. Fails when new unreachable functions
// appear that are not in the allow list. Every allow-list entry must have
// a [category] tag to prevent rubber-stamping.
//
// Usage:
//
//	go tool deadcode              check against allow list
//	go tool deadcode update       regenerate .deadcode-allow
//	go tool deadcode --json       check with JSON output
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"

	"zach.tools/go/devtools/allowlist"
)

// ///////////////////////////////////////////////
// Types
// ///////////////////////////////////////////////

// Entry is a normalized deadcode finding: file path and function name.
type Entry struct {
	File string `json:"file"`
	Func string `json:"func"`
}

// Report holds the diff between actual dead code and the allow list.
type Report struct {
	New     []Entry `json:"new"`
	Removed []Entry `json:"removed"`
	Total   int     `json:"total"`
}

// ///////////////////////////////////////////////
// Constants
// ///////////////////////////////////////////////

const allowFile = ".allow.deadcode"

var deadcodePattern = regexp.MustCompile(
	`^([^ ]+):\d+:\d+: unreachable func: (.+)$`,
)

var categories = []allowlist.Category{
	{Tag: "public-api", Description: "exported API not yet called from cmd/"},
	{Tag: "test-only", Description: "called from _test.go files; deadcode can't trace test imports"},
	{Tag: "platform", Description: "platform-specific code not built on current OS"},
	{Tag: "scaffold", Description: "framework wiring for future use"},
}

// String returns the canonical form used in .allow.deadcode.
func (e Entry) String() string { return e.File + " " + e.Func }

// ///////////////////////////////////////////////
// Entry Point
// ///////////////////////////////////////////////

func main() {
	mode := "check"
	jsonOutput := false

	for _, arg := range os.Args[1:] {
		switch arg {
		case "update":
			mode = "update"
		case "--json":
			jsonOutput = true
		case "--help", "-h":
			fmt.Fprintln(os.Stderr, "usage: go tool deadcode [update] [--json]")
			os.Exit(0)
		default:
			fmt.Fprintf(os.Stderr, "error: unknown argument %q\n", arg) //nolint:gosec // stderr, not HTTP
			os.Exit(2)
		}
	}

	switch mode {
	case "update":
		runUpdate()
	case "check":
		runCheck(jsonOutput)
	}
}

// ///////////////////////////////////////////////
// Commands
// ///////////////////////////////////////////////

// runUpdate regenerates .deadcode-allow from the current deadcode output.
func runUpdate() {
	fmt.Fprintln(os.Stderr, "Running deadcode ./...")
	actual := invokeDeadcode()

	var lines []string
	for _, e := range actual {
		lines = append(lines, e.String())
	}

	if err := allowlist.WriteUpdate(allowFile, "Deadcode", "make deadcode ARGS=update", categories, lines); err != nil {
		fmt.Fprintf(os.Stderr, "error: writing %s: %v\n", allowFile, err)
		os.Exit(2)
	}

	fmt.Fprintf(os.Stderr, "Allow list updated: %d entries in %s\n", len(actual), allowFile)
}

// runCheck compares current dead code against the allow list and fails on drift.
func runCheck(jsonOutput bool) {
	if _, err := os.Stat(allowFile); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s not found; run 'make deadcode ARGS=update' to create it\n", allowFile)
		os.Exit(1)
	}

	allowlist.FailOnUncategorized(allowFile)

	fmt.Fprintln(os.Stderr, "Running deadcode ./...")
	actual := invokeDeadcode()
	allowed := loadAllowed()

	actualSet := entrySet(actual)
	allowedSet := entrySet(allowed)

	var newEntries, removedEntries []Entry
	for _, e := range actual {
		if !allowedSet[e.String()] {
			newEntries = append(newEntries, e)
		}
	}
	for _, e := range allowed {
		if !actualSet[e.String()] {
			removedEntries = append(removedEntries, e)
		}
	}

	if jsonOutput {
		allowlist.WriteJSON(Report{
			New:     allowlist.Coalesce(newEntries),
			Removed: allowlist.Coalesce(removedEntries),
			Total:   len(actual),
		})
		if len(newEntries) > 0 {
			os.Exit(1)
		}
		return
	}

	exitCode := 0

	if len(newEntries) > 0 {
		fmt.Fprintf(os.Stderr, "\nFAIL: %d new unreachable function(s) not in allow list:\n", len(newEntries))
		for _, e := range newEntries {
			fmt.Fprintf(os.Stderr, "  %s\n", e)
		}
		fmt.Fprintf(os.Stderr, "\nFix: delete the function, wire it up, or add to %s.\n", allowFile)
		exitCode = 1
	}

	if len(removedEntries) > 0 {
		fmt.Fprintf(os.Stderr, "\n%d allow-list entries are no longer unreachable:\n", len(removedEntries))
		for _, e := range removedEntries {
			fmt.Fprintf(os.Stderr, "  %s\n", e)
		}
		fmt.Fprintf(os.Stderr, "\nRun 'make deadcode ARGS=update' to shrink the allow list.\n")
	}

	if exitCode == 0 && len(removedEntries) == 0 {
		fmt.Fprintf(os.Stderr, "%d known unreachable functions, no new dead code.\n", len(actual))
	}

	os.Exit(exitCode)
}

// ///////////////////////////////////////////////
// Deadcode Invocation
// ///////////////////////////////////////////////

// invokeDeadcode runs the deadcode binary and returns normalized, sorted entries.
func invokeDeadcode() []Entry {
	cmd := exec.Command("deadcode", "./...")
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
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].String() < entries[j].String()
	})
	return entries
}

// ///////////////////////////////////////////////
// Allow List
// ///////////////////////////////////////////////

// loadAllowed reads .deadcode-allow entries.
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
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].String() < entries[j].String()
	})
	return entries
}

func entrySet(entries []Entry) map[string]bool {
	m := make(map[string]bool, len(entries))
	for _, e := range entries {
		m[e.String()] = true
	}
	return m
}
