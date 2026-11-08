<p align="center">
  <img src="docs/public/lys-login.svg" width="160" alt="lys-login">
</p>

<h1 align="center">lys-login</h1>

<p align="center">
  The lys desktop's display manager, greeter and lock screen, in pure Go.
</p>

<p align="center">
  <a href="https://github.com/stubbedev/lys-login/actions/workflows/ci.yml"><img src="https://img.shields.io/github/actions/workflow/status/stubbedev/lys-login/ci.yml?branch=main&style=for-the-badge&label=CI" alt="CI"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue?style=for-the-badge" alt="MIT license"></a>
  <a href="https://stubbedev.github.io/lys-login/"><img src="https://img.shields.io/badge/docs-stubbedev.github.io-informational?style=for-the-badge" alt="Documentation"></a>
</p>

| Command | What it is |
| --- | --- |
| `lys-login daemon` | The display manager: a root service that shows the greeter in a lys-wm kiosk and starts sessions through PAM. |
| `lys-login greet` | The greeter, under `lys-login daemon` or under greetd in a cage kiosk. |
| `lys-login lock` | The session lock on `ext-session-lock-v1`, started by [lys-shell](https://github.com/stubbedev/lys-shell). |
| `lys-login upgrade` | Moves the running display manager to a new build without ending sessions. |

Both screens read the `login` section of the lys-shell config. A successful login
or unlock opens the login collection of
[lys-secrets](https://github.com/stubbedev/lys-secrets), else gnome-keyring.

## Install

NixOS: import `lys-login.nixosModules.default` from the flake and set

```nix
programs.lys-login.greeter.enable = true;
```

Debian, Ubuntu, Fedora: install the `.deb` or `.rpm` from the
[releases](https://github.com/stubbedev/lys-login/releases) together with lys-wm, then

```sh
sudo systemctl disable gdm
sudo systemctl enable lys-login
```

Other systems use the release archive or run the greeter under greetd. See the
[documentation](https://stubbedev.github.io/lys-login/) for every install path,
configuration, PAM setup and architecture.

## License

MIT. See [LICENSE](LICENSE).
