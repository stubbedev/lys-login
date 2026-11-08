# Debian, Ubuntu and Fedora

Each [release](https://github.com/stubbedev/lys-login/releases) carries `.deb`
(amd64, arm64) and `.rpm` (x86_64, aarch64) packages. Install with lys-wm and,
optionally, [lys-secrets](https://github.com/stubbedev/lys-secrets/releases), from
their own releases:

```sh
# Debian, Ubuntu (arm64: _arm64.deb)
sudo apt install ./lys-login_<version>_amd64.deb ./lys-wm_<version>_amd64.deb

# Fedora (aarch64: .aarch64.rpm)
sudo dnf install ./lys-login-<version>-*.x86_64.rpm ./lys-wm-<version>-*.x86_64.rpm
```

| | Debian, Ubuntu | Fedora |
| --- | --- | --- |
| Depends | `libpam0g`, `libpam-systemd`, `libx11-data`, `xkb-data`, `systemd` | `pam`, `libX11-common`, `xkeyboard-config`, `systemd`, `policycoreutils` |
| Recommends | `lys-wm`, `lys-secrets`, `accountsservice`, `libvulkan1`, `mesa-vulkan-drivers` | `lys-wm`, `lys-secrets`, `accountsservice`, `vulkan-loader`, `mesa-vulkan-drivers` |

## What it installs

- `/usr/bin/lys-login`;
- `lys-login.service`: the daemon on VT 1 with `/usr/bin/lys-wm`,
  `Alias=display-manager.service`, installed but not enabled;
- the `lys-greeter` system user (sysusers.d) and `/var/lib/lys-greeter`
  (tmpfiles.d);
- the PAM services `lys-login`, `lys-login-autologin` and `lys-shell`
  ([PAM services](/guide/pam)).

On Fedora with SELinux enabled, `/usr/bin/lys-login` is labelled `xdm_exec_t`;
the label is removed on uninstall.

## Enable

```sh
sudo systemctl disable gdm   # or your current display manager
sudo systemctl enable lys-login
```

It starts with `graphical.target`; on a system that boots to `multi-user.target`
also run `sudo systemctl set-default graphical.target`. Reboot, or stop the old
display manager (ending its session) and start `lys-login`. To revert, disable
`lys-login` and enable the old one.

The greeter reads `/etc/lys-shell/config.yaml`, which the package does not create;
see [Configuration](/guide/configuration).

The unit's arguments live in `LYS_LOGIN_DAEMON_ARGS`
([Command line](/reference/cli#lys-login-daemon)). To autologin, override it with
a drop-in (`systemctl edit lys-login`) that adds `--autologin USER`.

## Lock screen

Set the packaged PAM service in your lys-shell config (on Fedora the default
`system-auth` also works):

```yaml
login:
  lock:
    pam-service: lys-shell
```

## Upgrades

Upgrading the package reloads a running `lys-login.service`, which
[upgrades the daemon in place](/reference/architecture#upgrading-in-place). If
that fails the old daemon keeps running, the package still installs and prints a
notice; the reason is in `journalctl -u lys-login`. Retry with
`sudo systemctl reload lys-login`.

## lys-splash

The unit hands the screen over from [lys-splash](https://github.com/stubbedev/lys-splash)
without a blank frame when it is installed at `/usr/bin/lys-splash`, and skips
those steps otherwise.
