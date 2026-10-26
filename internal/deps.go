//go:build tools

// Package internal pins the third-party dependencies JailOps is allowed to use.
// Adding a dependency needs a reason in docs/design.md.
package internal

import (
	_ "filippo.io/age"
	_ "github.com/fergusstrange/embedded-postgres"
	_ "github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/klauspost/compress/zstd"
	_ "github.com/spf13/cobra"
	_ "golang.org/x/crypto/argon2"
	_ "golang.org/x/crypto/chacha20poly1305"
	_ "golang.org/x/crypto/hkdf"
	_ "golang.org/x/net/route"
	_ "golang.org/x/sys/unix"
	_ "golang.org/x/term"
	_ "gopkg.in/yaml.v3"
	_ "modernc.org/sqlite"
)
