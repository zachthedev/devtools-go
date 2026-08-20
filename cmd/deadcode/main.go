// Deadcode reports exported symbols that no main package can reach, and
// compares them against .allow.deadcode.
//
// The reachability analysis comes from golang.org/x/tools/cmd/deadcode.
// This wraps it with the allow list, the category rules, and the exit
// codes, and refuses to run without it rather than reporting an empty
// result.
//
// Declare it as a tool dependency to invoke it as `go tool deadcode`.
package main

import (
	"zach.tools/go/devtools/internal/driver"
	"zach.tools/go/devtools/internal/tools/deadcode"
)

func main() {
	driver.Main(deadcode.Tool())
}
