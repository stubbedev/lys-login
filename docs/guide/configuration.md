# Configuration

Both screens read the `login` section of the lys-shell config. The
[login reference](https://stubbedev.github.io/lys-shell/config/login) on the
lys-shell site is the schema; this page covers what lys-login does with it.
`lys-settings` edits these keys under **Login & Lock Screen** and **Greeter**.

## Which file

| Screen | Reads |
| --- | --- |
| Greeter | `/etc/lys-shell/config.yaml` (`--config` overrides), then `runtime.yaml` beside it |
| Lock screen | Your config, `~/.config/lys-shell/config.yaml` |

The greeter runs as an account that cannot read your home: copy or symlink your
config to the system path to share it. A config the greeter cannot load is
logged and the defaults are used.

```yaml
# /etc/lys-shell/config.yaml
styling:
  appearance: dark
login:
  background-mode: color
  background-color: "#1e1e2e"
  greeter:
    cursor-size: 24
```

## Keys

| Key | Greeter | Lock screen |
| --- | --- | --- |
| `styling`, `general.font-sans` | yes | yes |
| `login.background-mode`, `background-image`, `background-color`, `blur` | yes | yes |
| `wallpaper.wallpaper` (for `background-mode: wallpaper`) | yes | yes |
| `login.show-clock`, `clock-format`, `date-format` | yes | yes |
| `login.max-attempts`, `show-failed-attempts` | no | yes |
| `login.lock.*` | no | yes |
| `animations`, `bitwarden.unlock-with-session` | no | yes |
| `login.greeter.*` | yes | no |

The greeter applies no user stylesheet.

`login.greeter`:

| Key | Type | Default | Effect |
| --- | --- | --- | --- |
| `show-user-list` | bool | `true` | Show the [user list](/guide/getting-started#the-greeter). |
| `show-power-buttons` | bool | `true` | Show suspend, restart and shut down. |
| `cursor-theme` | string | `""` | Cursor theme; empty resolves it as described [below](#cursor). |
| `cursor-size` | int | `24` | Cursor size; when not set explicitly it is resolved like the theme. |
| `screen-reader` | string | `orca` | Program the accessibility menu starts. |

`login.lock` is described on [The lock screen](/guide/lock-screen#configuration).

## Change the login screen from your session

`lys-login apply-config <STAGED.yaml>` writes login keys to
`/etc/lys-shell/runtime.yaml`, leaving `config.yaml` and other tables in the
overlay untouched. The **Apply to login screen** button in `lys-settings` makes
the same change. The next greeter shows the change.

The staged file holds a `login` table or its keys at top level. Only these keys,
relative to `login`, are taken:

```text
background-mode  background-image  background-color  blur
show-clock  clock-format  date-format
greeter.show-user-list  greeter.show-power-buttons
greeter.cursor-theme  greeter.cursor-size
```

With `background-mode: image`, the image (at most 32 MiB) is read with your
rights and copied beside the overlay as `greeter-background<ext>`.

Who may run it:

- **Under `lys-login daemon`**: root and the user whose session the daemon runs,
  through `/run/lys-login/apply.sock`. Anyone else is asked by polkit for the
  action `dev.stubbe.lys.login.apply-config` (administrator authentication by
  default, kept for a while in an active session). The daemon checks the change
  before it asks, so an invalid one is refused without a prompt. The
  **Apply to login screen** button in `lys-settings` sends the change to this
  socket itself and says whether it was invalid, not authorized, or the
  authentication was dismissed.
- **Under greetd or without a session**: there is no daemon to ask. Root writes
  directly (`sudo lys-login apply-config`, `--config PATH` for another system
  config).


## Cursor

An explicit `login.greeter.cursor-theme` or `cursor-size` wins. Otherwise the
greeter uses the cursor of the last user to log in, or the only user. Theme and
size are resolved separately, first match wins:

1. **The session's record.** `lys-login record-cursor` sends `XCURSOR_THEME` and
   `XCURSOR_SIZE` (or `HYPRCURSOR_SIZE`) over `/run/lys-login/cursor.sock`. The
   daemon accepts it only from the user whose session it runs and stores it in
   `/var/lib/lys-login/cursor/<uid>`. lys-shell runs it at startup; under other
   sessions run it from the compositor's autostart. Not available under greetd.
2. **That user's dotfiles**, if the greeter account can enter the home
   (`chmod o+x "$HOME"`): Hyprland (`env = XCURSOR_*`, `hyprctl setcursor`,
   following `source =`), niri's `cursor` block, sway's
   `seat … xcursor_theme` (the remembered session's compositor first), then GTK
   `settings.ini` and `~/.icons/default/index.theme`.
3. **`XCURSOR_THEME` and `XCURSOR_SIZE`** in the environment, then the default.

Under the daemon the cursor goes into the greeter's lys-wm kiosk config. The theme
must be installed system-wide, where the greeter account can read it; otherwise
lys-wm draws its built-in arrow.

Under `displayManager = "greetd"` the NixOS module passes the system's
`XCURSOR_THEME` and `XCURSOR_SIZE` (`environment.sessionVariables`, then
`environment.variables`) with `XCURSOR_PATH=/run/current-system/sw/share/icons`
to cage and the greeter. On other greetd setups set these in the greetd command.

## Keyboard layout

Under the daemon the greeter uses the system layout. On each greeter start the
daemon reads `X11Layout`, `X11Model`, `X11Variant` and `X11Options` from
systemd-localed (2 s timeout), else `/etc/X11/xorg.conf.d/00-keyboard.conf`, else
`XKBLAYOUT`, `XKBMODEL`, `XKBVARIANT`, `XKBOPTIONS` in `/etc/vconsole.conf`, and
writes it to the kiosk config as `input.keyboard`. Set it with
`localectl set-x11-keymap de`, or `services.xserver.xkb.layout` on NixOS. A
layout lys-wm cannot compile is logged and lys-wm's US default is used.

With several layouts (`localectl set-x11-keymap de,us`) a chip in the top right
shows the active one. Clicking it or the XKB switch option (`grp:caps_toggle`,
`grp:alt_shift_toggle`, …) cycles layouts. The greeter remembers each user's
layout and switches to it when that user is chosen. Under greetd the chip only
displays the layout.

## Accessibility

The accessibility button beside the power buttons opens a menu (Tab to reach,
Space or Enter to toggle):

| Option | Effect |
| --- | --- |
| Screen reader | Starts `login.greeter.screen-reader` and serves the greeter over AT-SPI. Super+Alt+S toggles it. Hidden if the program is missing. |
| Large text | Scales text by 1.5. |
| High contrast | Uses the `prefers-contrast: more` styles. |
| Sticky keys | lys-wm's `input.keyboard.sticky_keys`. Hidden under greetd. |
| Slow keys | lys-wm's `slow_keys`, 300 ms. Hidden under greetd. |

Choices apply to the machine and persist to the next greeter. With every option
off, no accessibility process runs. The NixOS module enables
`services.gnome.at-spi2-core` by default for the screen reader.

## State

Kept beside the `--state` file (`/var/lib/lys-greeter/`, mode `0700`, owned by
`lys-greeter`, or `greeter` under greetd):

| File | Holds |
| --- | --- |
| `last-session` (the `--state` file) | Last session id |
| `last-user` | Last username |
| `last-layout/<user>` | Each user's keyboard layout |
| `accessibility` | Accessibility choices |
| `cache/` | The greeter's `XDG_CACHE_HOME` |
