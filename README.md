# devtools-go

Shared Go development tools for convention enforcement.

## Tools

| Tool       | Purpose                                                            |
| ---------- | ------------------------------------------------------------------ |
| `deadcode` | Wraps `golang.org/x/tools/cmd/deadcode` with allow-list management |
| `testpair` | Enforces 1:1 test file pairing and test function naming            |

Both tools use `allowlist` for shared allow-list infrastructure.

## Installation

Add tool directives to your `go.mod`:

```
tool (
    zach.tools/go/devtools/deadcode
    zach.tools/go/devtools/testpair
)
```

Then run:

```bash
go mod tidy
```

## Usage

```bash
# Check against allow list
go tool deadcode
go tool testpair ./cmd/... ./internal/...

# Regenerate allow list
go tool deadcode update
go tool testpair update ./cmd/... ./internal/...

# JSON output
go tool deadcode --json
go tool testpair --json ./cmd/... ./internal/...
```

## Allow List Format

Both tools use allow files (`.allow.deadcode`, `.allow.testpair`) with
category-tagged groups:

```
# [public-api]
internal/foo/bar.go SomeFunc

# [test-only]
internal/baz/qux.go HelperFunc
```

Every entry must be covered by a `# [category]` comment. A blank line resets
the category context. Entries without a category tag fail the check.

## Makefile Targets

Add these targets to your consumer repo's Makefile:

```makefile
deadcode:
	@go tool deadcode $(ARGS)

testpair:
	@go tool testpair $(ARGS) ./cmd/... ./internal/...
```
