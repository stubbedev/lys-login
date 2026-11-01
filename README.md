<p align="center">
  <img src="docs/public/lys-login.svg" width="160" alt="lys-login">
</p>

<h1 align="center">lys-login</h1>

<p align="center">
  The lys desktop's login screen: one screen in two modes, the greeter and the
  lock screen, plus a display manager of its own, in pure Go.
</p>

<p align="center">
  <a href="https://github.com/stubbedev/lys-login/actions/workflows/ci.yml"><img src="https://img.shields.io/github/actions/workflow/status/stubbedev/lys-login/ci.yml?branch=main&style=for-the-badge&label=CI" alt="CI"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue?style=for-the-badge" alt="MIT license"></a>
  <a href="https://stubbedev.github.io/lys-login/"><img src="https://img.shields.io/badge/docs-stubbedev.github.io-informational?style=for-the-badge" alt="Documentation"></a>
</p>

- `lys-login daemon`: the display manager, a root service that shows the greeter
  in a lys-wm kiosk and starts your session through PAM, with no greetd or cage.
- `lys-login greet`: the greeter, under `lys-login daemon` or under greetd in a
  cage kiosk.
- `lys-login lock`: the session lock on `ext-session-lock-v1`, which
  [lys-shell](https://github.com/stubbedev/lys-shell) starts.

It shares the desktop's look and its `login` config section with lys-shell. After
authenticating it unlocks the login collection of
[lys-secrets](https://github.com/stubbedev/lys-secrets), else a running
gnome-keyring.

## Install

On NixOS, add the flake as an input, import `lys-login.nixosModules.default` and
enable the module, which runs lys-login as the display manager:

```nix
programs.lys-login.greeter.enable = true;
```

Each [release](https://github.com/stubbedev/lys-login/releases) carries a `.deb`
and an `.rpm` for x86_64 and aarch64, and an archive for other systems. Install the package with
lys-wm, then switch display managers:

```sh
sudo systemctl disable gdm
sudo systemctl enable lys-login
```

For the lock screen, lys-shell runs `lys-login lock` from the PATH. Set
`login.lock.pam-service: lys-shell` in your lys-shell config, the PAM service the
packages install and `programs.lys-login.lock.enable` declares on NixOS.

## Documentation

The [documentation site](https://stubbedev.github.io/lys-login/) has
[install steps per distribution](https://stubbedev.github.io/lys-login/guide/getting-started#install),
[configuration](https://stubbedev.github.io/lys-login/guide/configuration),
[PAM services](https://stubbedev.github.io/lys-login/guide/pam),
[how the daemon, greeter and workers fit together](https://stubbedev.github.io/lys-login/reference/architecture)
and [development](https://stubbedev.github.io/lys-login/reference/development).

## The lys family

lys-login is part of the lys desktop. The `lys` menu of the
[documentation site](https://stubbedev.github.io/lys-login/) links the rest of the
family.

## License

MIT. See [LICENSE](LICENSE).
