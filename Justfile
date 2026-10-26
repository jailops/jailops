# Justfile for JailOps.

set shell := ["bash", "-euo", "pipefail", "-c"]

bins := "jailops jailopsd jailops-priv jailops-sync jailops-backupd"
ldflags := "-s -w -X main.version=$(tr -d '[:space:]' < version.txt)"

default: build

# ── Build ────────────────────────────────────────────────────────────────

# Native build of all binaries, version-stamped from version.txt.
build:
    mkdir -p bin
    for b in {{bins}}; do CGO_ENABLED=0 go build -ldflags="{{ldflags}}" -o bin/$b ./cmd/$b; done

# Cross-build for FreeBSD (the real target).
build-freebsd arch="amd64":
    mkdir -p bin
    for b in {{bins}}; do CGO_ENABLED=0 GOOS=freebsd GOARCH={{arch}} go build -ldflags="{{ldflags}}" -o bin/$b-freebsd-{{arch}} ./cmd/$b; done

# The forged-frame test tool (scripts/forge), for the PF matrix only: never shipped, never run outside a test jail.
build-forge arch="amd64":
    mkdir -p bin
    CGO_ENABLED=0 GOOS=freebsd GOARCH={{arch}} go build -o bin/forge-freebsd-{{arch}} ./scripts/forge

# jailopsd without the embedded web console (API only); the console is also switchable at run time with `web.enabled: false`.
build-noweb arch="amd64":
    mkdir -p bin
    CGO_ENABLED=0 GOOS=freebsd GOARCH={{arch}} go build -tags noweb -ldflags="{{ldflags}}" -o bin/jailopsd-noweb-freebsd-{{arch}} ./cmd/jailopsd

build-all: build (build-freebsd "amd64") (build-freebsd "arm64")
    ls -la bin/

# ── Web UIs (web/<app>, Vue 3 + Vite; registry in web/apps.json) ─────────
# Each app is embedded in its own binary: host -> internal/webui, backup -> internal/backupui.

# Build one UI app into its embed directory (default: the host console).
web APP="host":
    cd web && npm ci && npm run build -- --app {{APP}}

web-all:
    cd web && npm ci && npm run build:all

web-dev APP="host":
    cd web && npm run dev -- --app {{APP}}

web-test:
    cd web && npm run typecheck && npm run test:all

web-e2e APP="host":
    cd web && npm run e2e -- --app {{APP}}

# Add a new UI app from the template.
web-new NAME:
    cd web && npm run new-app -- {{NAME}}

# ── Quality ──────────────────────────────────────────────────────────────

fmt:
    gofmt -l -w .
    cd sdk && gofmt -l -w .
    go vet ./...
    cd sdk && go vet ./...

# Unit tests of both modules, with fakes. Runs anywhere.
test:
    go test ./... -count=1
    cd sdk && go test ./... -count=1

# Race detector.
test-race:
    go test ./... -race -count=1
    cd sdk && go test ./... -race -count=1

# Integration tests. Run on a FreeBSD host or VM with a scratch ZFS pool
# (JAILOPS_TEST_POOL). They fail, never skip, when a prerequisite is missing.
test-freebsd:
    go test -tags integration ./... -count=1 -run Integration

# A local trial of the release (archives and Linux packages in dist/), publishing nothing.
snapshot:
    goreleaser release --snapshot --clean --skip=before

# A key pair for signing the pkg repository: the private key stays with the release environment only
# (the PKG_REPO_KEY secret), the public one is published as https://pkg.jailops.net/jailops.pub.
pkg-keygen DIR="pkg-keys":
    mkdir -p {{DIR}} && umask 077 && openssl genrsa -out {{DIR}}/jailops-repo.key 4096
    openssl rsa -in {{DIR}}/jailops-repo.key -pubout -out {{DIR}}/jailops.pub
    @echo "keep {{DIR}}/jailops-repo.key secret; publish {{DIR}}/jailops.pub"

# Short fuzz run over the parsers.
fuzz seconds="20":
    go test ./internal/sys/zfs -run '^$' -fuzz FuzzParse -fuzztime {{seconds}}s

# Regenerate / verify the TypeScript wire types from sdk/api.
gen-ts:
    go run ./scripts/gen-ts

check-ts:
    go run ./scripts/gen-ts -check

check: fmt check-ts test

# Everything CI runs.
ci: fmt test build-freebsd

# ── Release ──────────────────────────────────────────────────────────────

# Cut a release, e.g. `just release v0.1.0`: version.txt, CHANGELOG.md, commit, tag, push master + tag. No tests here:
# CI runs them on every push and the release workflow checks gofmt and vet on the tag. Run by a person only.
release VERSION:
    bash scripts/release.sh {{VERSION}}

# The full local checks (gofmt, vet, wire types, FreeBSD cross-build) and what `release` would do; changes and pushes nothing.
release-dry VERSION:
    DRY=1 bash scripts/release.sh {{VERSION}}

clean:
    rm -rf bin dist
