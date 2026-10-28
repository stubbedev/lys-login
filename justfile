# lys-login dev tasks.

# List all recipes with their descriptions.
default:
    @just --list

# Run every release gate in order: vet, lint, test, race, build.
check: vet lint test race build

# Static analysis of every package with go vet.
vet:
    go vet ./...

# Lint without changing anything: formatting and golangci-lint
# (settings in .golangci.yml).
lint:
    golangci-lint fmt --diff
    golangci-lint run

# Run the test suite in a random order, so a test that leans on state an
# earlier one left behind fails instead of passing by declaration order.
test *args:
    go test -shuffle=on ./... {{args}}

# Run the test suite under the race detector, which needs cgo.
race:
    CGO_ENABLED=1 go test -race -shuffle=on ./...

# Compile-check every package; the output is discarded.
build:
    go build -o /dev/null ./...

# Format every Go source in place (gofumpt and gci via golangci-lint).
fmt:
    golangci-lint fmt

# lys-login as the display manager in a NixOS VM (needs KVM): the greeter
# in a lys-wm kiosk with no greetd or cage, a wrong and a right password,
# the logind session, lys-secrets' login collection unlocked by the
# login, and the greeter back after it. Screenshots in ./result.
login-vm:
    nix build -L .#checks.{{ arch() }}-{{ os() }}.login-vm

# Move lys-shell, lys-secrets, lys-ui and lys-lib to the heads of their
# main branches (the module proxy lags fresh commits), the flake's
# lys-shell and lys-secrets inputs with them, then the vendor hash.
bump-lys:
    GOPROXY=direct GOPRIVATE=github.com/stubbedev/* go get github.com/stubbedev/lys-shell@master github.com/stubbedev/lys-secrets@main github.com/stubbedev/lys-ui@main github.com/stubbedev/lys-lib@main
    go mod tidy
    nix flake update lys-shell lys-secrets lys-wm
    just vendor-hash

# Recompute nix/package.nix's vendorHash after a go.mod change: the
# vendor derivation is fixed-output, so a stale hash reuses the old
# module set instead of reporting the new one.
vendor-hash:
    #!/usr/bin/env bash
    set -euo pipefail
    file=nix/package.nix
    sed -i 's|vendorHash = "sha256-[^"]*";|vendorHash = "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=";|' "$file"
    got=$( (nix build .#default --no-link 2>&1 || true) | sed -n 's/^ *got: *\(sha256-[^ ]*\).*/\1/p' | head -1)
    if [ -z "$got" ]; then echo "no hash reported" >&2; exit 1; fi
    sed -i "s|vendorHash = \"sha256-[^\"]*\";|vendorHash = \"$got\";|" "$file"
    echo "vendorHash = $got"
