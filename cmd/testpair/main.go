// Testpair reports source files with no _test.go companion, test files with
// no source, and test functions named for a symbol their package does not
// declare. Findings are compared against .allow.testpair.
//
// Declare it as a tool dependency to invoke it as `go tool testpair`.
package main

import (
	"zach.tools/go/devtools/internal/driver"
	"zach.tools/go/devtools/internal/tools/testpair"
)

func main() {
	driver.Main(testpair.Tool())
}
