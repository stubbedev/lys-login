# Fedora

Each [release](https://github.com/stubbedev/lys-login/releases) carries an x86_64
and an aarch64 `.rpm`. Install it with lys-wm, which the display manager shows the greeter in, and
optionally [lys-secrets](https://github.com/stubbedev/lys-secrets/releases), from
their own releases:

```sh
sudo dnf install ./lys-login-<version>-*.x86_64.rpm ./lys-wm-<version>-*.x86_64.rpm
```

On aarch64 the files end in `.aarch64.rpm`.

The package needs `pam` and `systemd`, and recommends lys-wm, lys-secrets,
`vulkan-loader` and `mesa-vulkan-drivers`.

## What it installs

- `/usr/bin/lys-login`, which lys-shell runs for the lock screen;
- `lys-login.service`, the display manager on VT 1 with the greeter in
  `/usr/bin/lys-wm` (`Alias=display-manager.service`), installed but not enabled;
- the `lys-greeter` system user (sysusers.d) and its state directory
  `/var/lib/lys-greeter` (tmpfiles.d);
- two [PAM services](/guide/pam) written for the distribution: `lys-login`, which
  the display manager logs in through, and `lys-shell`, which the lock screen
  unlocks with.

With SELinux enabled, the package labels `/usr/bin/lys-login` as `xdm_exec_t`, a
display manager's type, and removes the label on uninstall.

## Switch display managers

Disable the current one and enable lys-login, for example:

```sh
sudo systemctl disable gdm
sudo systemctl enable lys-login
```

It starts with `graphical.target`. A system that boots to `multi-user.target` also
needs `sudo systemctl set-default graphical.target`. Reboot, or stop the old
display manager and start `lys-login`, which ends the session running on it. To go
back, disable `lys-login` and enable the old one.

The greeter reads `/etc/lys-shell/config.yaml`, which the package leaves to you:
see [Configuration](/guide/configuration).

## The lock screen

Fedora has the config's default service, `system-auth`, so the lock screen works as
it is. To use the packaged one, the name the NixOS and home-manager modules use too:

```yaml
login:
  lock:
    pam-service: lys-shell
```

## With lys-splash

The unit is written for [lys-splash](https://github.com/stubbedev/lys-splash) and
takes the screen from it without a blank frame when it is at `/usr/bin/lys-splash`.
Without it, those steps do nothing.
