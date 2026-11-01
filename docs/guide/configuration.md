# Configuration

The greeter and the lock screen read the `login` section of the lys-shell
config schema. The
[login reference](https://stubbedev.github.io/lys-shell/config/login) on the
lys-shell site lists every key with its type and default; this page says which
screen reads what, from where.

## Which file

| Screen | Reads | Why |
| --- | --- | --- |
| Greeter | `/etc/lys-shell/config.yaml` (`--config` overrides), then `runtime.yaml` beside it | It runs before anyone logs in, as an account that cannot read your home. |
| Lock screen | Your own lys-shell config, `~/.config/lys-shell/config.yaml` | It runs in your session. |

Copy or symlink your config to the system path if you want the greeter to follow
it. A config the greeter cannot load logs the reason and the greeter runs on the
defaults.

```yaml
# /etc/lys-shell/config.yaml
styling:
  appearance: dark
login:
  background-mode: color
  background-color: "#1e1e2e"
  greeter:
    show-user-list: true
    cursor-size: 24
```

The former top-level `greeter` and `lock` sections still load: each key moves to
its place in `login`, with a warning naming the new key.

## Who reads which key

| Key | Greeter | Lock screen |
| --- | --- | --- |
| `styling`, `general.font-sans` | yes | yes |
| `login.background-mode`, `background-image`, `background-color`, `blur` | yes | yes |
| `wallpaper.wallpaper`, for `background-mode: wallpaper` | yes | yes |
| `login.show-clock`, `clock-format`, `date-format` | yes | yes |
| `login.max-attempts`, `show-failed-attempts` | no | yes |
| `login.lock.*` | no | yes |
| `animations`, `bitwarden.unlock-with-session` | no | yes |
| `login.greeter.*` | yes | no |

The greeter carries no user stylesheet, as there is no user yet.
`lys-settings` edits these under **Login & Lock Screen** and **Greeter**.

## Change the login screen from your session

`lys-login apply-config <STAGED.yaml>` writes login keys to
`/etc/lys-shell/runtime.yaml`, so a hand-written `config.yaml` is never
clobbered and other tables in the overlay stay. The **Apply to login screen**
button in `lys-settings` runs it for you. The next greeter shows the change.

The staged file holds a `login` table, or its keys at the top level. Only these
keys are taken, relative to `login`:

```text
background-mode  background-image  background-color  blur
show-clock  clock-format  date-format
max-attempts  show-failed-attempts
greeter.show-user-list  greeter.show-power-buttons
greeter.cursor-theme  greeter.cursor-size
```

With `background-mode: image`, the image is read with your rights, up to 32 MiB,
and copied beside the overlay as `greeter-background<ext>`, so the greeter never
needs access to your files.

Who may run it depends on what runs the greeter:

- **Under `lys-login daemon`,** the user logged in through it, as themselves. It
  hands the change to the daemon over `/run/lys-login/apply.sock`, with no
  polkit. The daemon takes a change from root and from the user whose session it
  runs, so while the greeter is up only root can.
- **Under greetd, or with no session,** root: `sudo lys-login apply-config`, with
  `--config PATH` for another system config. When the button in `lys-settings`
  fails, it offers **Apply as administrator**, which does this through `pkexec`.

## The cursor

An explicit `login.greeter.cursor-theme` or `cursor-size` wins. Otherwise the
greeter shows the cursor of the last user to log in, or of the only user if
there is one. Size and theme are resolved separately, in this order:

1. **The record the session left.** `lys-login record-cursor` sends the session's
   `XCURSOR_THEME` and `XCURSOR_SIZE` (or `HYPRCURSOR_SIZE`) over
   `/run/lys-login/cursor.sock`. The daemon takes it only from the user whose
   session it runs and keeps it world-readable in
   `/var/lib/lys-login/cursor/<uid>`, so it works from a `0700` home and however
   the cursor was set. lys-shell runs it at startup; under another session, run
   it from the compositor's autostart. There is no record under greetd, which has
   no daemon to keep it.
2. **That user's dotfiles,** best effort, as the greeter's account must be able
   to enter the home (`chmod o+x "$HOME"`): the Hyprland config (`env =
   XCURSOR_*`, `hyprctl setcursor`, following `source =` includes), niri's
   `cursor` block, sway's `seat … xcursor_theme`, with the compositor of the
   remembered session first; then GTK's `settings.ini` and
   `~/.icons/default/index.theme`.
3. **The `XCURSOR_THEME` and `XCURSOR_SIZE` environment,** then the size default.

Under the daemon, the configured or recorded cursor goes into the greeter's lys-wm
kiosk config, so lys-wm draws it and the greeter inherits it. The daemon reads no
dotfiles itself, as it runs as root.

The theme itself has to be readable by the greeter's account, which a theme
installed only in your `0700` home is not: install it system-wide. With no theme
it can open, lys-wm draws its built-in arrow.

The NixOS module adds one thing under `displayManager = "greetd"`: it passes the
system's `XCURSOR_THEME` and `XCURSOR_SIZE`
(`environment.sessionVariables`, then `environment.variables`) to cage and the
greeter, with `XCURSOR_PATH=/run/current-system/sw/share/icons`, where a theme
from `environment.systemPackages` lands. On other greetd setups pass
`XCURSOR_THEME` and `XCURSOR_PATH` in the greetd command.

## What it remembers

The last session goes in the `--state` file and the last username in a
`last-user` file beside it. The display manager's unit and the NixOS module use
`/var/lib/lys-greeter/`, `0700` and owned by the greeter's account: `lys-greeter`,
or `greeter` under greetd.
