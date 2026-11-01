# Development

The dev shell (`nix develop`, or `direnv allow`) is lys-shell's: the pinned Go
toolchain, the lint stack, fonts, and the headless sway and private D-Bus the tests
start.

| Recipe | What it does |
| --- | --- |
| `just check` | Every release gate in order: vet, lint, test, race, build |
| `just test` | `go test -shuffle=on ./...` |
| `just race` | The suite under the race detector |
| `just lint` | Formatting and `golangci-lint run` against `.golangci.yml` |
| `just fmt` | Format every Go source in place |
| `just login-vm` | The display manager in a NixOS VM (needs KVM) |
| `just distro-vm <debian\|fedora>` | The `.deb` or `.rpm` built, installed and logged in through in a cloud VM (needs KVM and network in the guest) |
| `just bump-lys` | lys-shell, lys-secrets, lys-ui and lys-lib to their heads, then the vendor hash |
| `just vendor-hash` | Recompute `nix/package.nix`'s `vendorHash` after a go.mod change |
| `just dist` | The release binary in `dist/`, asking for the standard loader, with the wordlist notice |

`nix flake check` checks the package and the module and runs the login VM.

The greeter logs `greeter: ready: ...` once its first frame is on screen and
the compositor gives it the keyboard with a field of the form focused. A
compositor drops keys while no surface holds its keyboard, so a VM check waits
for that line before typing, and no pixel can stand in for it.

## CI and releases

`.github/workflows/ci.yml` runs `just check` and the package check, and builds the
archive, the `.deb` and the `.rpm` the way a release does (`package.yml`, with
`scripts/ci/` and `packaging/`). It installs and runs each package
(`scripts/ci/smoke-test.sh`), so a packaging break shows before a tag. The VMs need KVM
and run locally.

Pushing a tag `vX.Y.Z` runs `release.yml`: the same packages at that version, their
`SHA256SUMS`, and a GitHub release with generated notes.

`.github/workflows/security.yml` runs CodeQL (Go and GitHub Actions), Grype and
govulncheck, uploading findings to code scanning; dependency review gates pull requests.
Dependabot keeps the module and the workflows current.

## This site

```sh
cd docs
npm install
npm run dev
```

`.github/workflows/docs.yml` builds and deploys it on every push to `main`, with the
theme and workflow shared across the family from
[lys-docs-theme](https://github.com/stubbedev/lys-docs-theme).
