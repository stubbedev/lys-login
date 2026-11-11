# Command line

## lys-login greet

```text
lys-login greet [--config PATH] [--sessions DIR]... [--xsessions DIR]... [--state PATH] [--env KEY=VAL]... [--hand-over] [-- <session argv...>]
```

| Flag | Default | Effect |
| --- | --- | --- |
| `--config PATH` | `/etc/lys-shell/config.yaml` | lys-shell config to theme from. |
| `--sessions DIR` | `wayland-sessions` in each `$XDG_DATA_DIRS` directory: `/usr/local/share/wayland-sessions`, `/usr/share/wayland-sessions` when it names none | Wayland session directory. Repeatable; replaces the defaults. |
| `--xsessions DIR` | `xsessions` in each `$XDG_DATA_DIRS` directory: `/usr/local/share/xsessions`, `/usr/share/xsessions` when it names none | X11 session directory, started through `startx`. Repeatable; replaces the defaults. |
| `--state PATH` | `$XDG_STATE_HOME/lys-greeter/last-session`, else `$HOME/.local/state/lys-greeter/last-session`, else `/var/lib/lys-greeter/last-session`; a variable that holds no absolute path counts as unset | Last-session file; other [state](/guide/configuration#state) goes beside it. |
| `--env KEY=VAL` | | Extra session environment, applied after the `XDG_SESSION_TYPE` derived from the session kind. Repeatable. |
| `--hand-over` | off | Set by the daemon: after a login, keep showing the background until the daemon stops the compositor. Without it the greeter exits so greetd can start the session. |
| `-- <argv...>` | | Fallback session offered as "Custom" (type `wayland`). |

Exits 2 on a malformed command line or when there is no session to offer.

## lys-login daemon

```text
lys-login daemon [--vt N] [--service NAME] [--greeter-user NAME] [--compositor PATH] [--runtime-dir DIR] [--splash PATH] [--autologin USER [--autologin-session ID]] [-- <greet args...>]
```

Runs as root. Arguments after `--` go to `lys-login greet`; an empty session list
stops it at startup.

| Flag | Default | Effect |
| --- | --- | --- |
| `--vt N` | `1` | Virtual terminal. |
| `--service NAME` | `lys-login` | [PAM service](/guide/pam). |
| `--greeter-user NAME` | `lys-greeter` | Account the greeter runs as. |
| `--compositor PATH` | `lys-wm` | The greeter's compositor. |
| `--runtime-dir DIR` | `/run/lys-login` | Sockets and kiosk config. |
| `--splash PATH` | | lys-splash, quit with `--retain-splash` once the greeter's first frame is presented. |
| `--autologin USER` | | Log USER in once per boot via `lys-login-autologin` ([Autologin](/guide/pam#autologin)). |
| `--autologin-session ID` | last session, else first | Session to autologin into (a session file's name, `-x11` suffix for X11). An unknown ID, or this flag without `--autologin`, stops the daemon at startup. |

The packaged unit's `ExecStart` and `ExecReload` both use
`LYS_LOGIN_DAEMON_ARGS`:

```text
--vt 1 --service lys-login --greeter-user lys-greeter --compositor /usr/bin/lys-wm --splash /usr/bin/lys-splash -- --config /etc/lys-shell/config.yaml --state /var/lib/lys-greeter/last-session
```

## lys-login upgrade

```text
lys-login upgrade [-- <daemon args...>]
```

Run as root from the new build, normally by `systemctl reload lys-login`. Moves
the running daemon to this build and these arguments
([Upgrading in place](/reference/architecture#upgrading-in-place)) and prints the
program it now runs.

| Exit | Meaning |
| --- | --- |
| 0 | The new daemon took over. |
| 1 | The upgrade failed; the reason is printed and the old daemon keeps running. |
| 2 | Arguments the daemon would refuse. |

## lys-login lock

```text
lys-login lock
```

No arguments. Output and exit codes:
[The lock screen](/guide/lock-screen#interface-with-lys-shell).

## lys-login apply-config

```text
lys-login apply-config [--config PATH] <STAGED.yaml>
```

Changes the login screen and prints the file written. `--config` is root-only.
See [Configuration](/guide/configuration#change-the-login-screen-from-your-session).

## lys-login record-cursor

```text
lys-login record-cursor
```

Sends the session's cursor to the daemon and prints the file written. Exits 0
without recording when the session names no cursor. See
[Configuration](/guide/configuration#cursor).

## lys-login switch-user

```text
lys-login switch-user
```

In a session started by the daemon: locks the running sessions and shows a
greeter on a free VT. See [Switching users](/reference/architecture#switching-users).

## Internal modes

`lys-login worker`, `lys-login daemon-resume` and `lys-login vault-unlock` are
started by lys-login itself.

## Environment

| Variable | Effect |
| --- | --- |
| `LYS_UI_RENDERER` | `cpu` always draws on the CPU; `gpu` makes an unusable GPU an error; unset or `auto` picks. |
| `XCURSOR_THEME`, `XCURSOR_SIZE` | Last step of [cursor resolution](/guide/configuration#cursor). |
