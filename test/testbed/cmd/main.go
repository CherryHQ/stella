// Command testbed runs a disposable local Stella instance for API and browser tests.
package main

import (
	"os"

	"github.com/CherryHQ/stella/test/testbed"
)

func main() { os.Exit(testbed.RunCLI(os.Args[1:])) }
