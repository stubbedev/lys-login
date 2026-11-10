# Other systems

Run the display manager from the release archive, or only the greeter under greetd.

## Display manager from the archive

`lys-login-<version>-<arch>-linux.tar.gz` (`x86_64`, `aarch64`) holds the binary,
unit, sysusers and tmpfiles files, the polkit action, and both distributions' PAM
files under `pam/`.
The binary uses the standard program interpreter; the unit expects
`/usr/bin/lys-login` and `/usr/bin/lys-wm` (lys-wm has its own archive in
[its releases](https://github.com/stubbedev/lys-wm/releases)). From the unpacked
directory:

```sh
sudo install -m 0755 lys-login /usr/bin/lys-login
sudo install -m 0644 lys-login.service /usr/lib/systemd/system/
sudo install -D -m 0644 lys-login.sysusers /usr/lib/sysusers.d/lys-login.conf
sudo install -D -m 0644 lys-login.tmpfiles /usr/lib/tmpfiles.d/lys-login.conf
sudo install -D -m 0644 dev.stubbe.lys.login.policy /usr/share/polkit-1/actions/dev.stubbe.lys.login.policy
sudo systemd-sysusers lys-login.conf
sudo systemd-tmpfiles --create lys-login.conf
sudo systemctl daemon-reload
```

Write `/etc/pam.d/lys-login` (and `lys-login-autologin` for autologin) from the
closest file under `pam/` ([PAM services](/guide/pam)), then:

```sh
sudo systemctl disable gdm
sudo systemctl enable lys-login
```

## Greeter under greetd

```toml
# /etc/greetd/config.toml
[default_session]
command = "cage -s -- lys-login greet --config /etc/lys-shell/config.yaml --state /var/lib/lys-greeter/last-session"
user = "greeter"
```

```text
greetd → cage -s → lys-login greet → (login) → your session
```

- Create the state directory:
  `install -d -o greeter -g greeter -m 0700 /var/lib/lys-greeter`.
- Sessions come from `/usr/local/share/wayland-sessions`,
  `/usr/share/wayland-sessions` and the matching `xsessions`; `--sessions DIR`
  and `--xsessions DIR` replace them.
- `-- sway` appends a fallback session offered as "Custom".
- There is no daemon, so `record-cursor` and the `lys-settings` apply button are
  unavailable, and `apply-config` needs root.

### home-manager

`lys-login.homeManagerModules.default` options, under `programs.lys-login`:

| Option | Type | Default | Effect |
| --- | --- | --- | --- |
| `enable` | bool | `false` | Install `lys-login`. |
| `package` | package | this flake's | The lys-login package. |
| `lock.enable` | bool | `false` | Warn with the steps to create the lock screen's PAM service. |
| `lock.pamService` | string | `"lys-shell"` | The service name used in that warning. |
| `greeter.enable` | bool | `false` | Install cage and the `lys-greeter-session` wrapper, and warn with the greetd setup left to do. |
| `greeter.cagePackage` | package | `pkgs.cage` | The kiosk compositor. |
| `greeter.renderer` | `"auto"` or `"software"` | `"auto"` | `software` bakes the CPU/pixman variables into the wrapper. |
| `greeter.settings` | YAML | `{}` | Written to `$XDG_CONFIG_HOME/lys-shell/greeter.yaml`; copy it where the greetd user can read it. |

`lys-greeter-session` runs cage and the greeter, passing its arguments to
`lys-login greet`:

```text
lys-greeter-session --config /etc/lys-shell/config.yaml --state /var/lib/lys-greeter/last-session
```

### Rendering without a GPU

The greeter draws on the CPU when Vulkan is unavailable; `LYS_UI_RENDERER=cpu`
forces it. The display manager's lys-wm renders with Vulkan and stops on a card
without a Vulkan driver, such as a VM's virtio-gpu; `LYS_WM_RENDERER=gles` makes
it render with GLES, on llvmpipe when no GPU driver is there. The greeter's
lys-wm gets its environment from PAM, not from the unit, so put the line in
`/etc/environment` (read by `pam_env`), where lys-wm sessions find it too.
For cage set `WLR_RENDERER=pixman` and `WLR_DRM_NO_MODIFIERS=1`.
The home-manager `greeter.renderer = "software"` sets the cage and greeter
variables. On a non-NixOS host, run the
nix-built package under [nixGL](https://github.com/nix-community/nixGL) for GPU
rendering:

```text
nixGLIntel lys-greeter-session --config /etc/lys-shell/config.yaml --state /var/lib/lys-greeter/last-session
```
