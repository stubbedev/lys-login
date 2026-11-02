# lys-login dev tasks.

# List all recipes with their descriptions.
default:
    @just --list

# Run every release gate in order: vet, lint, test, race, build.
check: vet lint no-fused test race build

# Static analysis of every package with go vet.
vet:
    go vet ./...

# Lint without changing anything: formatting and golangci-lint
# Fail on any float product fused into an FMA, which rounds differently from amd64.
no-fused:
    no-fused

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

# The program interpreter the dist/ binary requests: the distro's loader
# at its standard path. purego links it dynamically, and a Go toolchain
# from Nix would otherwise name its own /nix/store loader, which exists
# only on NixOS.
fhs_interpreter := if arch() == "aarch64" { "/lib/ld-linux-aarch64.so.1" } else { "/lib64/ld-linux-x86-64.so.2" }

# Build the release binary into dist/, as the archive and the Debian and
# RPM packages ship it: pure Go (libpam and the drawing libraries load at
# runtime through purego), stripped, asking for the standard loader;
# beside it the notice of the wordlist lys-shell's config links in.
dist:
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -I {{fhs_interpreter}}" -o dist/ ./cmd/lys-login
    install -m 0644 "$(go list -m -f '{{{{.Dir}}' github.com/stubbedev/lys-shell)/service/bitwarden/generator/eff_large_wordlist.NOTICE" dist/

# Format every Go source in place (gofumpt and gci via golangci-lint).
fmt:
    golangci-lint fmt

# lys-login as the display manager in a NixOS VM (needs KVM): the greeter
# in a lys-wm kiosk with no greetd or cage, a wrong and a right password,
# the logind session, lys-secrets' login collection unlocked by the
# login, and the greeter back after it. Screenshots in ./result.
login-vm:
    nix build -L .#checks.{{ arch() }}-{{ os() }}.login-vm

# The login keyring with the real gnome-keyring and no PAM module for it, in a
# NixOS VM (needs KVM): the daemon starts and unlocks it, a password it no
# longer opens with warns, and so does no keyring at all.
keyring-vm:
    nix build -L .#checks.{{ arch() }}-{{ os() }}.keyring-vm

# The greeter under greetd in a cage kiosk in a NixOS VM (needs KVM), on GLES2
# over llvmpipe and on pixman: a wrong and a right password, and the greeter back.
greetd-vm:
    nix build -L .#checks.{{ arch() }}-{{ os() }}.greetd-vm

# An X11 session through the display manager in a NixOS VM (needs KVM): logind
# holds it as Type=x11, and the greeter before it stays wayland.
x11-session-vm:
    nix build -L .#checks.{{ arch() }}-{{ os() }}.x11-session-vm

# The .deb or the .rpm built, installed and logged in through in a
# Debian or Fedora cloud VM, as nix/distro-vm.nix describes (needs KVM,
# and the network in the guest): `just distro-vm debian`.
distro-vm distro: dist
    ./scripts/ci/build-archive.sh dist 0.0.0 {{ arch() }}
    nix run .#distro-vm-{{ distro }} -- lys-login-0.0.0-{{ arch() }}-linux.tar.gz

# Move lys-shell, lys-secrets, lys-ui and lys-lib to the heads of their
# main branches (the module proxy lags fresh commits), the flake's
# lys-shell and lys-secrets inputs with them, then the vendor hash.
bump-lys:
    GOPROXY=direct GOPRIVATE=github.com/stubbedev/* go get github.com/stubbedev/lys-shell@master github.com/stubbedev/lys-secrets@main github.com/stubbedev/lys-ui@main github.com/stubbedev/lys-lib@main
    go mod tidy
    nix flake update lys-shell lys-secrets lys-wm
    just vendor-hash

# Set nix/package.nix's vendorHash to the hash of the modules go.sum pins.
vendor-hash:
    nix run github:stubbedev/lys-ci#vendor-hash -- nix/package.nix .#default.goModules

# Tag the current commit as the next release and push it, refusing anything but
# a clean default branch whose CI succeeded.
release-patch:
    nix run github:stubbedev/lys-ci#release -- patch

release-minor:
    nix run github:stubbedev/lys-ci#release -- minor

release-major:
    nix run github:stubbedev/lys-ci#release -- major
