# Other systems

There are two ways to run it on a system without a package: the display manager from
the release archive, or only the greeter, under greetd.

## The display manager from the archive

The release's `lys-login-<version>-<arch>-linux.tar.gz` (`x86_64` or `aarch64`) holds the files the packages
install, with both distributions' PAM services under `pam/`. The binary asks for
the standard program interpreter, and the unit names `/usr/bin/lys-login` and
`/usr/bin/lys-wm`; lys-wm has an archive of its own in
[its releases](https://github.com/stubbedev/lys-wm/releases). From the unpacked
directory:

```sh
sudo install -m 0755 lys-login /usr/bin/lys-login
sudo install -m 0644 lys-login.service /usr/lib/systemd/system/
sudo install -D -m 0644 lys-login.sysusers /usr/lib/sysusers.d/lys-login.conf
sudo install -D -m 0644 lys-login.tmpfiles /usr/lib/tmpfiles.d/lys-login.conf
sudo systemd-sysusers lys-login.conf
sudo systemd-tmpfiles --create lys-login.conf
sudo systemctl daemon-reload
```

Then write `/etc/pam.d/lys-login` for your distribution from the file under `pam/`
closest to it ([PAM services](/guide/pam)), and switch display managers:

```sh
sudo systemctl disable gdm
sudo systemctl enable lys-login
```

## The greeter under greetd

Point greetd's `default_session` at the greeter, hosted by cage:

```toml
# /etc/greetd/config.toml
[default_session]
command = "cage -s -- lys-login greet --config /etc/lys-shell/config.yaml --state /var/lib/lys-greeter/last-session"
user = "greeter"
```

greetd starts the kiosk compositor, whose single client is the greeter, and replaces
it with your chosen session on login:

```text
greetd → cage -s → lys-login greet → (login) → your compositor
```

- The greeter runs as the greetd user with no usable home, so its config lives at a
  system path, `/etc/lys-shell/config.yaml` by default.
- Create the state directory, writable by the greetd user:
  `install -d -o greeter -g greeter -m 0700 /var/lib/lys-greeter`.
- Sessions come from `/usr/local/share/wayland-sessions` and
  `/usr/share/wayland-sessions`, and the same for `xsessions`. `--sessions DIR` and
  `--xsessions DIR` override them.
- Append an explicit fallback session with `-- sway`. It is offered as "Custom", and
  is the only entry when there are no session files.

Under greetd there is no daemon, so `lys-login record-cursor` has nothing to talk to
and `apply-config` needs root. See [Configuration](/guide/configuration).

### home-manager

`lys-login.homeManagerModules.default` with `programs.lys-login.greeter.enable`
installs cage and a `lys-greeter-session` wrapper, cage and the greeter in one
command, to point greetd at. It passes its arguments on to `lys-login greet`:

```text
lys-greeter-session --config /etc/lys-shell/config.yaml --state /var/lib/lys-greeter/last-session
```

home-manager cannot manage greetd or PAM, so the module warns with what is left to
do. Its `greeter.settings` go to `$XDG_CONFIG_HOME/lys-shell/greeter.yaml`; copy them
somewhere the greetd user can read. `programs.lys-login.enable` alone installs the
binary for the lock screen.

### Rendering without a GPU

The login screen needs no GPU: it draws on the CPU when Vulkan is not available.
`LYS_UI_RENDERER=cpu` forces that. For cage, set `WLR_RENDERER=pixman` and
`WLR_DRM_NO_MODIFIERS=1`. The home-manager wrapper sets all three with
`greeter.renderer = "software"`. This suits VMs and hosts where GPU setup is
unreliable.

On a host that is not NixOS, the nix-built package needs
[nixGL](https://github.com/nix-community/nixGL) around the whole launch for the GPU:

```text
nixGLIntel lys-greeter-session --config /etc/lys-shell/config.yaml --state /var/lib/lys-greeter/last-session
```
