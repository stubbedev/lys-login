# Development

`nix develop` (or `direnv allow`) gives lys-shell's dev shell: the pinned Go
toolchain, the lint stack, fonts, and the headless sway and private D-Bus the
tests use.

| Recipe | Effect |
| --- | --- |
| `just check` | Release gates in order: vet, lint, no-fused, test, race, build. |
| `just vet` | `go vet ./...` |
| `just lint` | `golangci-lint fmt --diff` and `golangci-lint run` (`.golangci.yml`). |
| `just no-fused` | Fail on float products fused into FMA. |
| `just test [args]` | `go test -shuffle=on ./...` |
| `just race` | Tests under the race detector. |
| `just build` | Compile every package. |
| `just fmt` | Format all Go sources (gofumpt, gci). |
| `just dist` | Release binary in `dist/`, pure Go, with the standard loader and the wordlist notice. |
| `just distro-vm <debian\|fedora>` | Build, install and log in through the `.deb` or `.rpm` in a cloud VM (KVM, guest network). |
| `just bump-lys` | Move lys-shell, lys-secrets, lys-ui and lys-lib to their heads, update flake inputs and the vendor hash. |
| `just vendor-hash` | Recompute `vendorHash` in `nix/package.nix`. |
| `just release-patch\|minor\|major` | Tag and push the next release from a clean default branch with green CI. |

NixOS VM checks (KVM), each `just <name>`:

| Check | Covers |
| --- | --- |
| `login-vm` | Daemon, greeter in lys-wm, wrong and right password, logind session, lys-secrets unlock, greeter returns. |
| `greetd-vm` | Greeter under greetd and cage, on GLES2/llvmpipe and on pixman. |
| `keyring-vm` | gnome-keyring started and unlocked without its PAM module; warnings for a rejected password and no keyring. |
| `upgrade-vm` | In-place upgrade with a session and lock screen; refused and failed upgrades. |
| `switch-vm` | Fast user switching, upgrade with two sessions, logout, stop. |
| `autologin-vm` | Autologin, greeter after logout, LUKS passphrase opening the keyring. |
| `rekey-vm` | A changed password re-keys the login collection. |
| `keyboard-vm` | System layout, layout switching, per-user layout. |
| `lock-vm` | Lock screen status row, layout switch, fprintd match and mismatch. |
| `accessibility-vm` | Accessibility menu, Super+Alt+S with Orca, persistence. |
| `x11-session-vm` | X11 session registered as `x11`, greeter as `wayland`. |
| `wallclock-vm` | Setting the wall clock updates the login clock. |

`nix flake check` runs every check in `nix/checks.nix`.

The VM checks show the greeter on virtio-gpu, which has no Vulkan driver, so
`nix/tests/greeter-vm.nix` sets `LYS_WM_RENDERER=gles` in the session variables
for the greeter's lys-wm and the lys-wm sessions.

The greeter logs `greeter: ready: ...` once its first frame is on screen and it
holds keyboard focus on a form field. VM checks wait for that line before typing.

## CI and releases

| Workflow | Does |
| --- | --- |
| `ci.yml` | `just check` and the package check on x86_64 and aarch64; the `login`, `keyring`, `upgrade`, `greetd`, `wallclock` and `x11-session` VMs; builds and smoke-tests the archive, `.deb` and `.rpm` via `package.yml`. |
| `release.yml` | On a `vX.Y.Z` tag: the packages, `SHA256SUMS` and a GitHub release. |
| `security.yml` | CodeQL, Grype, govulncheck; dependency review on PRs. |
| `vendor-hash.yml` | Updates `vendorHash` on PRs that change `go.mod`/`go.sum`. |
| `docs.yml` | Builds and deploys this site on pushes to `main` that touch `docs/`. |

Dependabot updates Go modules, workflows and the site's npm packages.

## This site

```sh
cd docs
npm install
npm run dev
```

The theme and docs workflow come from
[lys-docs-theme](https://github.com/stubbedev/lys-docs-theme).
