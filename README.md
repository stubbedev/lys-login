# lys-login

lys-login is the lys desktop's login screen: one screen in two modes, the
greeter and the lock screen, plus a display manager of its own.

- `lys-login greet`: the greeter, under greetd in a cage kiosk or under
  `lys-login daemon`.
- `lys-login lock`: the session lock on `ext-session-lock-v1`, which
  lys-shell starts for its lock triggers (logind's Lock signal, idle,
  `lys-shell lock`, lock-on-start).
- `lys-login daemon`: the display manager in place of greetd and cage.
- `lys-login unlock-keyring`: the display manager's hand-off of the login
  password to the new session's keyring (below).
- `lys-login apply-config`: changes the login screen the system shows, from
  your session (below).
- `lys-login record-cursor`: hands your session's cursor to the display
  manager for the next greeter (below).

It shares the desktop's look and its `login` config section with
[lys-shell](https://github.com/stubbedev/lys-shell), whose `config`,
`styling`, `i18n` and widget packages it builds on. After authenticating
it unlocks the login collection of
[lys-secrets](https://github.com/stubbedev/lys-secrets) (else a running
gnome-keyring).

## The lock screen

lys-shell runs `lys-login lock` from the PATH. On NixOS,
`programs.lys-login.enable = true;` installs it and
`programs.lys-login.lock.enable = true;` adds the PAM service the unlock
authenticates against (`lock.pamService`, `lys-shell` by default; the
config's `login.lock.pam-service` must name it). Elsewhere install the
binary and point `login.lock.pam-service` at an existing service such as
`system-auth`.

The contract with lys-shell (`shell/lockclient` there): `lys-login lock`
takes no arguments, prints `locked` on stdout once the compositor confirms
the lock, and exits 0 once the session unlocks, 3 when another lys-login
already holds the session locked, 4 when the lock never took, and 1 on any
other failure. `cmd/lys-login/lock_test.go` pins it against the real binary.

## The greeter

`lys-login greet` is a [greetd](https://sr.ht/~kennylevinsen/greetd/) greeter — a
display-manager login screen that can replace sddm/gdm for Wayland sessions. It
is the same login screen as the lock screen, in its greeter mode: both read the
`login` config section, so they share the theme, background, blur, clock and
the failed-attempt handling; `login.greeter` holds what only the greeter has
(user list, power buttons, cursor). It is editable under **Login & Lock Screen**
and **Greeter** in `lys-settings`.

Features:

- **Session picker** — discovers Wayland sessions from
  `wayland-sessions/*.desktop` and X11 sessions from `xsessions/*.desktop` (the
  same files sddm/gdm read) and offers them in a dropdown. X11 entries are
  labelled `(X11)` and launched through `startx`. The last-used session is
  remembered and pre-selected.
- **User list** — real login accounts (from `/etc/passwd`) shown as clickable
  avatars; avatars come from AccountsService
  (`/var/lib/AccountsService/icons/<user>`) or `~/.face`, with an
  initial-letter fallback. Hidden when there are more than 8 users (the
  username field always works).
- **Remembered username** — the last successful username is pre-filled, with
  focus on the password field.
- **Caps Lock warning** — shown under the password entry while Caps Lock is on.
- **Power controls** — shutdown and reboot buttons.
- **Localized** — greeter labels follow the system locale (same Fluent-based
  i18n as the shell).

### Configuration

The greeter reads the `login` section of its config file (see below for where
that file lives per setup); the full list is in the
[config reference](/config/login):

| Key | Default | What it does |
| --- | --- | --- |
| `background-mode` | `"color"` | `color`, `image`, or `wallpaper` (reuses `wallpaper.wallpaper`). |
| `background-image` | `""` | Image path for `background-mode: image`. |
| `background-color` | `"#000000"` | Fill color for `background-mode: color`. |
| `blur` | `0` | Blur radius for image and wallpaper backgrounds. |
| `show-clock` | `true` | Show the clock above the login form. |
| `clock-format` / `date-format` | `"%H:%M"` / `"%A, %B %-d"` | strftime formats. |
| `greeter.show-user-list` | `true` | Clickable avatars for login users. |
| `greeter.show-power-buttons` | `true` | Shutdown/reboot buttons at the bottom. |
| `greeter.cursor-theme` | `""` (auto) | Xcursor theme. Empty = auto-detect (see below). |
| `greeter.cursor-size` | `24` (auto) | Logical cursor size; scaled per display, so HiDPI outputs get a sharp cursor (hotplug included). Unset = auto-detect. |

The former `greeter` and `lock` sections still load: each key is moved to its
place in `login` with a warning naming the new key.

When `greeter.cursor-theme` / `greeter.cursor-size` are not set, the greeter shows the
cursor the last logged-in user actually sees. The primary source is the record
the user's session hands the display manager with `lys-login record-cursor`
(lys-shell runs it at startup; under another session run it from the
compositor's autostart). It sends the session's live `XCURSOR_THEME` /
`XCURSOR_SIZE` (size falling back to `HYPRCURSOR_SIZE`) over
`/run/lys-login/cursor.sock`; the daemon takes it from the user whose session
it runs, and from nobody else, and keeps it world-readable in
`/var/lib/lys-login/cursor/<uid>`. That is exactly what the compositor
exported, so the login screen keeps the last session's cursor even when it was
set via env or `hyprcursor` rather than a config file, wherever the session's
`XDG_STATE_HOME` points and however closed the home is (homes are `0700` by
default on NixOS, Debian and Fedora). The daemon puts the cursor into the
greeter's lys-wm kiosk config, so lys-wm draws it, and the greeter inherits it.

Without a record the greeter falls back to reading dotfiles: the Hyprland
config (`env = XCURSOR_*` lines, `hyprctl setcursor`, following `source =`
includes), niri's `cursor` block, sway's `seat … xcursor_theme`, GTK
`settings.ini`, and `~/.icons/default/index.theme` — preferring the compositor
of the remembered session. That is best-effort: it only works when that user's
home is readable by the greeter's account (e.g. `chmod o+x` on `$HOME`), which
is also the only source under greetd, which has no daemon to keep records;
otherwise the `XCURSOR_THEME`/`XCURSOR_SIZE` environment and finally the
defaults apply.

> **Cursor theme must be reachable by the greeter.** The greeter runs as the
> unprivileged `greeter` user (home `/var/empty`), so a theme installed only via
> home-manager (in your `0700` home) is invisible to it — and with *no* theme
> resolvable it shows the large built-in fallback cursor, which ignores the size.
> On NixOS the module inherits the system cursor automatically: set it the usual
> way (`environment.sessionVariables.XCURSOR_THEME` / `XCURSOR_SIZE`) and install
> the theme system-wide (`environment.systemPackages`, so it lands in
> `/run/current-system/sw/share/icons`, which is on the greeter's `XCURSOR_PATH`).
> An explicit `greeter.settings.login.greeter.cursor-theme` still overrides. On other
> setups install the theme system-wide and pass `XCURSOR_THEME` / `XCURSOR_PATH`
> in the greetd command.

These options live under **Login & Lock Screen** and **Greeter** in `lys-settings`. The
greeter reads the *system* config (`/etc/lys-shell/config.yaml` plus a
`runtime.yaml` overlay beside it), not your user config. The **Apply to login
screen** button on that page pushes the current greeter values to the system
with `lys-login apply-config <STAGED.yaml>`, which writes only the login keys
the greeter reads, and an image background copied beside them, to
`/etc/lys-shell/runtime.yaml`, so a hand-written `config.yaml` is never
clobbered. It needs no polkit:

- **Under `lys-login daemon`** the user logged in through it runs it as
  themselves. It reads the staged file and the image with that user's rights
  and hands them to the daemon over `/run/lys-login/apply.sock`; the daemon
  takes a change from root and from the user whose session it runs, and
  from nobody else (nobody at all while the greeter is up). The next greeter
  shows it.
- **Under greetd**, or with no session, root runs it (`sudo lys-login
  apply-config`, `--config PATH` for another system config) and writes the
  overlay itself.

### How it runs

There are two ways to run it.

**Under greetd** (the default): greetd starts a kiosk compositor
([cage](https://github.com/cage-kiosk/cage)) whose single client is the
greeter. On successful login greetd replaces the greeter with your chosen
session.

```
greetd → cage -s → lys-login greet → (login) → your compositor
```

**As its own display manager** (`lys-login daemon`): a root daemon on a VT
shows the greeter in a lys-wm kiosk (a generated lys-wm config with no
binds, the greeter as its one program) and starts the session itself. PAM
is the only dependency: no greetd, no cage. Every login runs in a worker
process of its own that holds the PAM transaction and leads the logind
session pam_systemd registers; the greeter runs as the unprivileged
`lys-greeter` user and talks to the daemon in the greetd protocol over a
socket only it can reach, so it is the same greeter in both setups.

```
lys-login daemon → worker (greeter session) → lys-wm kiosk → lys-login greet
                 → worker (your login)       → your compositor
```

When the session ends the greeter comes back. A wrong password keeps the
greeter up for the next attempt.

The password you log in with also unlocks your login keyring: once the
session starts, its worker runs `lys-login unlock-keyring` as you with the
password on a pipe, which hands it to
[lys-secrets](https://github.com/stubbedev/lys-secrets) (else gnome-keyring)
as soon as one runs in the new session, so the login collection is open
before the first app asks for a secret. It waits up to a minute for a
keyring to start (lys-secrets' user unit with the graphical session, or D-Bus
activation by the first client); the login never waits for it, and without a
keyring it gives up and logs why.

### NixOS

Enable it through the system module:

```nix
programs.lys-login.greeter = {
  enable = true;
  # Theme it like your desktop (full lys-shell config schema; the greeter honours
  # styling, login.* and wallpaper).
  settings = {
    styling.appearance = "dark";
    login.background-mode = "color";
    login.background-color = "#1e1e2e";
    login.greeter.cursor-size = 24;
  };
};
```

To run it as its own display manager instead of under greetd:

```nix
programs.lys-wm.enable = true;
programs.lys-login.greeter = {
  enable = true;
  displayManager = "lys-login";
  compositorPackage = config.programs.lys-wm.package;
};
```

This adds the `lys-login` PAM service, the `lys-greeter` system user and a
`display-manager.service` running `lys-login daemon` on `greeter.vt` (1),
and leaves greetd and cage out.

Sessions are discovered from the aggregate NixOS sessions directory, which
contains the session files of every installed Wayland compositor. Options:

| Option | Default | What it does |
| --- | --- | --- |
| `greeter.session.dirs` | NixOS sessions dir | Directories scanned for Wayland `*.desktop` session files. |
| `greeter.session.x11Dirs` | NixOS xsessions dir | Directories scanned for X11 session files (launched via `startx`; `[]` disables). |
| `greeter.session.command` | `""` | Optional explicit fallback session, offered as a "Custom" entry. |
| `greeter.session.environment` | `[]` | Extra `KEY=value` entries for the started session. |
| `greeter.renderer` | `"auto"` | `auto` tries GPU and falls back to software; `software` forces the driver-free pixman + cairo path. |
| `greeter.graphicsWrapper` | `[]` | Command prefix (e.g. a nixGL wrapper) around the whole launch — for non-NixOS-style driver setups. |

The last session/username are stored in `/var/lib/lys-greeter/`, owned by the
greetd user.

### Debian, Ubuntu and Fedora

Each [release](https://github.com/stubbedev/lys-login/releases) carries a
`.deb` and an `.rpm` (x86_64) with:

- `/usr/bin/lys-login`, which lys-shell runs for the lock screen;
- `lys-login.service`, the display manager on VT 1 with the greeter in
  lys-wm (`Alias=display-manager.service`), installed but not enabled;
- the `lys-greeter` system user (sysusers.d) and its state directory
  `/var/lib/lys-greeter` (tmpfiles.d);
- two PAM services written for the distribution: `lys-login`, which the
  display manager logs in through (the usual display-manager stack, with
  pam_systemd from the common session), and `lys-shell`, which the lock
  screen unlocks with. Set `login.lock.pam-service: lys-shell` in your
  lys-shell config, the name the NixOS and home-manager modules use too.

lys-wm and lys-secrets are recommended. To switch display managers, disable
the current one and enable lys-login, for example:

```
sudo systemctl disable gdm
sudo systemctl enable lys-login
```

The release's `lys-login-<version>-x86_64-linux.tar.gz` holds the same
files, with both distributions' PAM services under `pam/`, for other
systems.

### Other distros (manual greetd)

Point greetd's `default_session` at the greeter, hosted by cage:

```toml
# /etc/greetd/config.toml
[default_session]
command = "cage -s -- lys-login greet --config /etc/lys-shell/config.yaml --state /var/lib/lys-greeter/last-session"
user = "greeter"
```

- The greeter runs pre-login as the greetd user (no `$HOME`), so its theme
  config lives at a system path — `/etc/lys-shell/config.yaml` by default.
- Create the state dir and make it writable by the greetd user:
  `install -d -o greeter -g greeter -m 0700 /var/lib/lys-greeter`.
- Sessions are discovered from `/usr/local/share/wayland-sessions` and
  `/usr/share/wayland-sessions`; add `--sessions DIR` to override.
- An explicit fallback session can be appended: `-- sway` (offered as
  "Custom", and the only entry when no `.desktop` sessions exist).

With home-manager, `programs.lys-login.greeter.enable` installs a
`lys-greeter-session` wrapper (cage + greeter in one command) you can point
greetd at directly.

#### Software rendering / nixGL

The login screen needs no GPU: with `renderer = "software"` (or manually
`WLR_RENDERER=pixman WLR_DRM_NO_MODIFIERS=1 GSK_RENDERER=cairo GDK_DISABLE=gl`)
the whole stack renders without any GPU driver — useful on VMs or hosts where GL
init is unreliable.

On non-NixOS hosts running the nix-built package, GPU acceleration needs
[nixGL](https://github.com/nix-community/nixGL) wrapping the whole launch:

```
nixGLIntel lys-greeter-session --config /etc/lys-shell/config.yaml --state /var/lib/lys-greeter/last-session
```

### CLI reference

```
lys-login greet [--config PATH] [--sessions DIR]... [--xsessions DIR]... [--state PATH] [--env KEY=VAL]... [-- <session argv...>]
```

| Flag | Default | What it does |
| --- | --- | --- |
| `--config PATH` | `/etc/lys-shell/config.yaml` | lys-shell config used for theming. |
| `--sessions DIR` | `/usr/{local/,}share/wayland-sessions` | Wayland session `.desktop` dir (repeatable; overrides defaults). |
| `--xsessions DIR` | `/usr/{local/,}share/xsessions` | X11 session `.desktop` dir (repeatable; launched via `startx`). |
| `--state PATH` | `$XDG_STATE_HOME/lys-greeter/last-session` | File the last session id is remembered in (username in a `last-user` sibling). |
| `--env KEY=VAL` | — | Extra environment for the started session (repeatable). |
| `-- <argv...>` | — | Optional explicit fallback session ("Custom"). |

## Development

| Recipe | What it does |
| --- | --- |
| `just check` | Every release gate in order: vet, lint, test, race, build |
| `just test` | `go test -shuffle=on ./...` |
| `just race` | The suite under the race detector |
| `just lint` | Formatting and `golangci-lint run` against `.golangci.yml` |
| `just login-vm` | The display manager in a NixOS VM (needs KVM) |
| `just bump-lys` | lys-shell, lys-secrets, lys-ui and lys-lib to their heads, then the vendor hash |
| `just vendor-hash` | Recompute `nix/package.nix`'s `vendorHash` after a go.mod change |
| `just dist` | The release binary in `dist/`, asking for the standard loader, with the wordlist notice |

The dev shell (`nix develop`, or `direnv allow`) is lys-shell's: the pinned
Go toolchain, the lint stack, fonts, and the headless sway and private D-Bus
the tests start. `nix flake check` checks the package and runs the login VM.

## CI and releases

`.github/workflows/ci.yml` runs `just check` and the package check, and builds
the archive, the `.deb` and the `.rpm` the way a release does (`package.yml`,
with `scripts/ci/` and `packaging/`). It installs and runs each package
(`scripts/ci/smoke-test.sh`), so a packaging break shows before a tag. The
login VM needs KVM and runs locally (`just login-vm`, `nix flake check`).
Pushing a tag `vX.Y.Z` runs `release.yml`: the same packages at that
version, their `SHA256SUMS`, and a GitHub release with generated notes.

`.github/workflows/security.yml` runs CodeQL (Go and GitHub Actions), Grype and
govulncheck, uploading findings to code scanning; dependency review gates
pull requests. Dependabot keeps the module and the workflows current.
