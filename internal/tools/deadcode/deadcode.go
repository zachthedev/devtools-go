// Package deadcode implements the deadcode devtool. Wraps the upstream
// deadcode binary (golang.org/x/tools/cmd/deadcode) and compares its
// output against .allow.deadcode.
//
// Findings are compared against the allow list. See the driver package
// for the check/update/validate/--json lifecycle.
package deadcode

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"

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

const (
	allowFile = ".allow.deadcode"

	// binEnv overrides which analyzer binary runs, so a CI job can point
	// at a known build without placing it first on PATH.
	binEnv = "DEADCODE"

	// defaultBin is the analyzer name as go install leaves it on PATH.
	defaultBin = "deadcode"

	// toolPath is the analyzer module path, used to invoke a build the
	// consuming module owns.
	toolPath = "golang.org/x/tools/cmd/deadcode"

	// analysisTimeout bounds one analyzer run. Whole-program reachability
	// on a large module is slow, so this is generous; it exists to stop a
	// hang, not to police a slow machine.
	analysisTimeout = 15 * time.Minute

	// probeTimeout bounds the go tool lookup, which may compile the
	// analyzer the first time it runs.
	probeTimeout = 5 * time.Minute

	installCmd = "go install golang.org/x/tools/cmd/deadcode@latest"
	adoptCmd   = "go get -tool golang.org/x/tools/cmd/deadcode"
)

var (
	// The path is matched lazily rather than as a run of non-spaces,
	// because a package directory may contain one. A space-delimited path
	// would leave such a line unparsed, and an unparsed line ends the run.
	deadcodePattern = regexp.MustCompile(`^(.+?):\d+:\d+: unreachable func: (.+)$`)

	// skewPattern matches the loader's report that the analyzer predates
	// the language version a package declares. The analyzer is a separate
	// binary from the toolchain that builds the module, so upgrading
	// either one alone leaves the pair unable to work together.
	skewPattern = regexp.MustCompile(`requires newer Go version go(\d+\.\d+).*built with go(\d+\.\d+)`)
)

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
		UpdateCmd:        "go tool deadcode update",
		RequireAllowFile: true,
		Categories: []allowlist.Category{
			{Tag: "public-api", Description: "exported API not yet called from cmd/"},
			{Tag: "test-only", Description: "called from _test.go files; deadcode cannot trace test imports"},
			{Tag: "platform", Description: "platform-specific code not built on current OS"},
			{Tag: "scaffold", Description: "framework wiring for future use"},
		},
		Gather:    invokeDeadcode,
		LoadAllow: loadAllowed,
		ToFinding: func(e Entry) report.Finding {
			return report.Finding{Kind: "unreachable", File: e.File, Detail: e.Func}
		},
		AllowPath: allowPath,
	}
}

// allowPath reports the file path in one .allow.deadcode line, whose shape
// is "<file> <func>". The path comes first here and second in testpair's
// file, which is why each tool states its own.
func allowPath(line string) string {
	file, _, _ := strings.Cut(line, " ")
	return file
}

// ///////////////////////////////////////////////
// Deadcode Invocation
// ///////////////////////////////////////////////

// analyzer reports the command that runs the unreachable-function
// analysis, and the label to name it by in a message.
//
// A module that declares the analyzer as a tool dependency has it built by
// whichever toolchain is in use, so the analyzer and the code it reads
// always agree on the language version. A binary on PATH is installed once
// and drifts from every toolchain upgrade after that, which is the drift
// [diagnose] reports. The full module path is what distinguishes it from
// this wrapper, which also answers to the name deadcode.
func analyzer() (name string, args []string, label string) {
	return resolveAnalyzer(os.Getenv(binEnv), moduleBuildsAnalyzer)
}

// moduleBuildsAnalyzer reports whether the current module declares the
// analyzer as a tool dependency, which is what lets go tool build it.
func moduleBuildsAnalyzer() bool {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	return exec.CommandContext(ctx, "go", "tool", "-n", toolPath).Run() == nil
}

// resolveAnalyzer picks between an explicit override, a build the module
// owns, and a binary on PATH, in that order. An override is a deliberate
// choice and outranks both; a module-owned build outranks PATH because it
// cannot fall behind the toolchain.
func resolveAnalyzer(override string, moduleBuilds func() bool) (name string, args []string, label string) {
	if override != "" {
		return override, nil, override
	}
	if moduleBuilds() {
		return "go", []string{"tool", toolPath}, "go tool " + toolPath
	}
	return defaultBin, nil, defaultBin
}

// invokeDeadcode runs the analyzer against the given patterns and returns
// normalized, sorted entries.
//
// Findings arrive on stdout and diagnostics on stderr, so the two streams
// are captured apart. A merged stream would feed diagnostics to the
// finding parser, and a parser that skipped what it could not match would
// report an analyzer that never ran as a repository with no dead code.
func invokeDeadcode(patterns []string) []Entry {
	name, args, label := analyzer()

	ctx, cancel := context.WithTimeout(context.Background(), analysisTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, slices.Concat(args, patterns)...) //nolint:gosec // args are go list patterns
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			fmt.Fprintf(os.Stderr, "error: %s did not finish within %s\n", label, analysisTimeout)
			os.Exit(2)
		}
		fail(label, err, stderr.String())
	}

	// An analyzer that complains and still exits zero has something to say
	// about the analysis it just did. Holding the captured stream back
	// would leave that run looking unremarkable.
	if s := strings.TrimSpace(stderr.String()); s != "" {
		fmt.Fprintf(os.Stderr, "warning: %s wrote to stderr:\n\n%s\n\n", label, report.Block(s))
	}

	entries, unparsed := parseOutput(stdout.String())
	if len(unparsed) > 0 {
		unrecognized(label, unparsed)
	}
	return entries
}

// fail explains why the analyzer produced no usable findings, then exits 2.
func fail(label string, err error, stderr string) {
	fmt.Fprintf(os.Stderr, "error: %s\n", diagnose(label, err, stderr))
	if s := strings.TrimSpace(stderr); s != "" {
		fmt.Fprintf(os.Stderr, "\n%s\n", report.Block(s))
	}
	os.Exit(2)
}

// diagnose renders the one-line explanation for an analyzer that failed.
//
// Any non-zero status is a failure. The analyzer prints findings and exits
// zero, reserving a non-zero status for a fatal error such as "packages
// contain errors". Reading a status as "found something" would leave a
// broken analyzer indistinguishable from a clean repository.
func diagnose(label string, err error, stderr string) string {
	switch {
	case errors.Is(err, exec.ErrNotFound):
		return fmt.Sprintf("%s not installed. Run %s, or build it from this module with %s",
			label, installCmd, adoptCmd)
	case skewPattern.MatchString(stderr):
		m := skewPattern.FindStringSubmatch(stderr)
		return fmt.Sprintf("%s was built with go%s and cannot analyze a module requiring go%s. Rebuild it with %s, or build it from this module with %s",
			label, m[2], m[1], installCmd, adoptCmd)
	default:
		return fmt.Sprintf("%s failed: %v", label, err)
	}
}

// unrecognized reports analyzer output the finding parser does not
// understand, then exits 2.
//
// Only findings reach stdout, so a line that does not parse means the
// output format moved out from under [deadcodePattern]. Dropping such a
// line would shrink the finding set silently and pass the check on the
// strength of what it failed to read.
func unrecognized(label string, lines []string) {
	fmt.Fprintf(os.Stderr, "error: %s produced %s this build cannot parse:\n\n",
		label, report.Count(len(lines), "line", ""))
	for _, line := range lines {
		fmt.Fprintf(os.Stderr, "  %s\n", report.Block(line))
	}
	fmt.Fprintf(os.Stderr, "\nThe analyzer output format moved. Upgrade this tool, or pin the analyzer.\n")
	os.Exit(2)
}

// parseOutput extracts file/func pairs from raw deadcode stdout. It
// returns the parsed entries sorted, plus any non-blank line it could not
// match so the caller can refuse to act on a partial read.
func parseOutput(output string) (entries []Entry, unparsed []string) {
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		line = strings.ReplaceAll(line, "\\", "/")
		m := deadcodePattern.FindStringSubmatch(line)
		if m == nil {
			unparsed = append(unparsed, line)
			continue
		}
		entries = append(entries, Entry{File: m[1], Func: m[2]})
	}
	slices.SortFunc(entries, func(a, b Entry) int { return cmp.Compare(a.String(), b.String()) })
	return entries, unparsed
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
	var (
		entries   []Entry
		malformed []string
	)
	for _, line := range lines {
		file, fn, ok := strings.Cut(line, " ")
		if !ok || strings.TrimSpace(fn) == "" {
			malformed = append(malformed, line)
			continue
		}
		entries = append(entries, Entry{File: file, Func: fn})
	}
	// Skipping a line here would shrink the allow list without saying so,
	// and the exception count printed on a clean pass would then disagree
	// with the file it names.
	if len(malformed) > 0 {
		report.PrintMalformed(os.Stderr, malformed, allowFile)
		os.Exit(2)
	}
	slices.SortFunc(entries, func(a, b Entry) int { return cmp.Compare(a.String(), b.String()) })
	return entries
}
