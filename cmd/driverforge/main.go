// Command driverforge is the Driverforge CLI for building and managing Control4
// drivers. See internal/cli for the command tree.
package main

import (
	"os"

	"github.com/driverforge/cli/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
