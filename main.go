// Command ddslop finds duplicated code and proposes refactoring candidates.
package main

import (
	"os"

	"github.com/heikki-laitala/ddslop/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
