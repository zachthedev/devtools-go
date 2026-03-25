// Testpair is a single-tool entry point for the test-pairing check.
// Use this when your repo only needs one devtool and you'd rather
// invoke `go tool testpair` than `go tool devtools testpair`.
// The meta-dispatcher at ./cmd/devtools exposes the same tool.
package main

import (
	"zach.tools/go/devtools/internal/driver"
	"zach.tools/go/devtools/internal/tools/testpair"
)

func main() {
	driver.Main(testpair.Tool())
}
