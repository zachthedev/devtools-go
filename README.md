# devtools-go

Shared Go devtools for convention enforcement. Each tool is a library
under `internal/tools/`; a thin `main` package under `cmd/<tool>` exposes
it to consumers through `go.mod` tool directives.

## Layout

```
cmd/
  testpair/       # go tool testpair
  deadcode/       # go tool deadcode
internal/
  allowlist/      # allow-file format: category-tagged groups, drift detection
  driver/         # check/update/validate flow, argv parsing, exit codes
  report/         # text, diff, and JSON rendering (fatih/color for terminals)
  scope/          # go-list pattern resolution, path matching, result filtering
  tools/
    testpair/     # 1:1 _test.go pairing + TestSymbol_* naming enforcement
    deadcode/     # unreachable-function detection via x/tools/cmd/deadcode
```

Consumers pin only the tools they actually use — one `tool` directive
per tool in `go.mod`. Unused tools never enter the consumer's module
graph, so adding a new devtool here never forces a leaf to rebuild.

## Subcommand contract

Every tool follows the same lifecycle:

| Subcommand  | Effect                                                                 |
| ----------- | ---------------------------------------------------------------------- |
| _(default)_ | Check current findings against the allow file. Fails on new drift.     |
| `update`    | Regenerate the allow file from current findings.                       |
| `validate`  | Verify the allow file's format only. No package scan. Fast; hook-safe. |

### Flags

| Flag           | Effect                                                         |
| -------------- | -------------------------------------------------------------- |
| `--json`       | Emit machine-readable diff report to stdout.                   |
| `--json=PATH`  | Emit the JSON report to PATH. Directory must exist.            |
| `--quiet`      | Suppress the clean-pass summary line.                          |
| `--diff`       | Render check output as a unified +/- diff.                     |
| `--color=W`    | Force color: `auto` (default), `on`, `off`. Honors `NO_COLOR`. |
| `--help`, `-h` | Show tool usage.                                               |

Positional arguments are `go list` patterns (`./cmd/...`, `./internal/foo/...`).
With no positional argument, the tool defaults to `./...`. Scoped runs skip
out-of-scope entries in the allow file and ignore out-of-scope uncategorized
tags, so a focused run doesn't trip on drift in untouched parts of the repo.

### Exit codes

| Code | Meaning                                                        |
| ---- | -------------------------------------------------------------- |
| `0`  | No new findings.                                               |
| `1`  | Policy drift (new findings or uncategorized allow-file tags).  |
| `2`  | Invocation error (missing tool binary, bad flag, I/O failure). |

## Installation

Consumer `go.mod`, pinning the tools the project actually needs:

```go
tool (
    zach.tools/go/devtools/cmd/testpair
    zach.tools/go/devtools/cmd/deadcode
)
```

Then `go mod tidy`.

## Usage

```bash
go tool testpair                            # check ./...
go tool testpair ./cmd/... ./internal/...   # scoped check
go tool testpair update                     # regenerate allow file
go tool testpair validate                   # allow-file format check only
go tool testpair --json                     # JSON output to stdout
go tool testpair --json=report.json         # JSON output to a file
go tool testpair --diff                     # unified diff output

go tool deadcode                            # same flags apply
```

## Allow-file format

Allow files are line-based. Each entry must sit under a `# [category]`
comment; a blank line resets the category context so the next group has
to declare its own tag. Entries without a preceding category tag fail
the check, preventing rubber-stamped additions.

```
# Tool allow list.
# Categories:
#   [public-api]  exported API not yet called from cmd/
#   [test-only]   called from _test.go files

# [public-api]
internal/foo/bar.go SomeFunc
internal/foo/baz.go OtherFunc

# [test-only]
internal/qux/helper.go Helper
```

The category set is declared per tool in `internal/tools/<name>/<name>.go`.
The allow-file header lists them so reviewers know which tags apply.

## Makefile integration

```makefile
testpair:
	@go tool testpair $(ARGS) ./cmd/... ./internal/...

deadcode:
	@go tool deadcode $(ARGS) ./cmd/... ./internal/...
```

Pass a subcommand or flag through `ARGS`, e.g. `make testpair ARGS=update`
or `make deadcode ARGS="--json=report.json"`.

## Authoring a new tool

1. Add `internal/tools/<name>/<name>.go` exposing a `Tool() *driver.Tool[T]`
   factory that declares the tool's name, allow-file path, categories, and
   `Gather` / `LoadAllow` / `ToFinding` callbacks.
2. Add `cmd/<name>/main.go` (a 12-line wrapper that imports the library
   and calls `driver.Main(pkg.Tool())`).
3. Write `internal/tools/<name>/<name>_test.go` using the existing
   table-driven pattern.

The driver handles argv, scoping, diff, JSON, exit codes, and color for
free. A new tool should only carry domain logic (what counts as a
finding) and allow-file serialization (how to format and parse one
entry). Consumers that want the new tool add it to their `go.mod` tool
directive; other consumers see no change.
