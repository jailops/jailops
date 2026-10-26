// Command jailops-sync applies a git repository of YAML to a fleet of JailOps
// hosts through the API. It uses only the SDK; see internal/fleet/cli.
package main

import (
	"os"

	"jailops.net/jailops/internal/fleet/cli"
)

// version is set with -ldflags -X main.version.
var version = "dev"

func main() {
	os.Exit(cli.Run(cli.Env{
		Stdin:   os.Stdin,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Version: version,
	}, os.Args[1:]))
}
