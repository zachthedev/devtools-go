// Deadcode is a single-tool entry point for the unreachable-function check.
// Use this when your repo only needs one devtool and you'd rather
// invoke `go tool deadcode` than `go tool devtools deadcode`.
// The meta-dispatcher at ./cmd/devtools exposes the same tool.
package main

import (
	"zach.tools/go/devtools/internal/driver"
	"zach.tools/go/devtools/internal/tools/deadcode"
)

func main() {
	driver.Main(deadcode.Tool())
}
