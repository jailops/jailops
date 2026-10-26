// Command jailopsd is the per-host JailOps daemon: API, authentication, state,
// reconcile and operations. It runs unprivileged and reaches the system
// through jailops-priv.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"jailops.net/jailops/internal/daemon"
)

// version is set with -ldflags "-X main.version=...".
var version = "dev"

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	fs := flag.NewFlagSet("jailopsd", flag.ContinueOnError)
	cfgPath := fs.String("config", daemon.DefaultConfigPath, "path of the configuration file")
	dev := fs.Bool("dev", false, "development: run the privileged operations in process (needs root)")
	checkOnly := fs.Bool("check-config", false, "validate the configuration and exit")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: jailopsd [flags]\n       jailopsd version\n\nflags:\n")
		fs.PrintDefaults()
	}
	if len(args) > 0 && args[0] == "version" {
		fmt.Println("jailopsd", version)
		return 0
	}
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return 2
	}
	if *checkOnly {
		if _, err := daemon.Load(*cfgPath); err != nil {
			fmt.Fprintln(os.Stderr, "jailopsd:", err)
			return 1
		}
		fmt.Println("configuration is valid")
		return 0
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	// SIGHUP is ignored: the configuration is read at start.
	signal.Ignore(syscall.SIGHUP)
	if err := daemon.Run(ctx, daemon.Options{ConfigPath: *cfgPath, Dev: *dev, Version: version}); err != nil {
		fmt.Fprintln(os.Stderr, "jailopsd:", err)
		return 1
	}
	return 0
}
