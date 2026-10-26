// Command jailops-backupd is the JailOps backup node: a keyless server that stores
// encrypted, deduplicated backups of jails on a directory or an S3 bucket, with
// its control plane in PostgreSQL. It holds no key and never decrypts anything.
package main

import (
	"context"
	"os"

	"jailops.net/jailops/internal/backupd"
)

func main() {
	os.Exit(backupd.RunCLI(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
