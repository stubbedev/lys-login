# Command line

## lys-login greet

```text
lys-login greet [--config PATH] [--sessions DIR]... [--xsessions DIR]... [--state PATH] [--env KEY=VAL]... [--hand-over] [-- <session argv...>]
```

| Flag | Default | What it does |
| --- | --- | --- |
| `--config PATH` | `/etc/lys-shell/config.yaml` | The lys-shell config used for theming. |
| `--sessions DIR` | `/usr/local/share/wayland-sessions`, `/usr/share/wayland-sessions` | A Wayland session directory. Repeatable; replaces the defaults. |
| `--xsessions DIR` | `/usr/local/share/xsessions`, `/usr/share/xsessions` | An X11 session directory, launched through `startx`. Repeatable; replaces the defaults. |
| `--state PATH` | `$XDG_STATE_HOME/lys-greeter/last-session`, else under `$HOME/.local/state`, else `/var/lib/lys-greeter/last-session` | The file holding the last session id, with the username in a `last-user` file beside it. |
| `--env KEY=VAL` | | Extra environment for the started session, after `XDG_SESSION_TYPE`, which the greeter sets from the session's kind (`wayland` for a `-- <argv>` session). Repeatable. |
| `--hand-over` | off | Set by `lys-login daemon`: after a login the greeter stays up showing only its background until the daemon stops its compositor. Under greetd leave it off, and the greeter exits so greetd starts the session. |
| `-- <argv...>` | | An explicit fallback session, offered as "Custom". |

It exits 2 on a malformed command line, or with no session to offer.

## lys-login daemon

```text
lys-login daemon [--vt N] [--service NAME] [--greeter-user NAME] [--compositor PATH] [--runtime-dir DIR] [--splash PATH] [--autologin USER [--autologin-session ID]] [-- <greet args...>]
```

Run as root by its service. Everything after `--` goes to `lys-login greet`, and a
session list that would be empty stops it at startup.

| Flag | Default | What it does |
| --- | --- | --- |
| `--vt N` | `1` | The virtual terminal. |
| `--service NAME` | `lys-login` | The [PAM service](/guide/pam). |
| `--greeter-user NAME` | `lys-greeter` | The account the greeter runs as. |
| `--compositor PATH` | `lys-wm` | The greeter's compositor. |
| `--runtime-dir DIR` | `/run/lys-login` | The directory for the sockets and the kiosk config. |
| `--splash PATH` | | lys-splash, quit with `--retain-splash` once the greeter's first frame is presented. |
| `--autologin USER` | | Log USER in once per boot, before the first greeter, through the `lys-login-autologin` [PAM service](/guide/pam#autologin). After USER logs out, the greeter shows. |
| `--autologin-session ID` | the last session, else the first offered | The session the autologin starts: a session file's name, `-x11` for X11 ones. An ID no session has stops the daemon at startup. |

The packaged unit keeps these arguments in `LYS_LOGIN_DAEMON_ARGS`, for its `ExecStart`
and its `ExecReload` alike:

```text
--vt 1 --service lys-login --greeter-user lys-greeter --compositor /usr/bin/lys-wm --splash /usr/bin/lys-splash -- --config /etc/lys-shell/config.yaml --state /var/lib/lys-greeter/last-session
```

## lys-login upgrade

```text
lys-login upgrade [-- <daemon args...>]
```

Run as root from the new build, usually by `systemctl reload lys-login`. It moves the
running daemon to that build and those arguments in place, and prints the program the
daemon now runs. [Upgrading in place](/reference/architecture#upgrading-in-place) says
what stays and what changes.

It exits 0 once the new daemon has taken over. It exits 1 with the reason when the upgrade
fails, and the old daemon keeps running. It exits 2 on arguments the daemon would refuse.

## lys-login lock

```text
lys-login lock
```

No arguments. [The lock screen](/guide/lock-screen#the-contract-with-lys-shell) lists
its output and exit codes.

## lys-login apply-config

```text
lys-login apply-config [--config PATH] <STAGED.yaml>
```

Changes the login screen and prints the file it wrote. `--config` is root's: a user's
change goes to the display manager, which writes the config it shows. See
[Configuration](/guide/configuration#change-the-login-screen-from-your-session).

## lys-login record-cursor

```text
lys-login record-cursor
```

Hands the session's cursor to the display manager and prints the file it wrote. A session
that names no cursor records nothing and exits 0. See
[Configuration](/guide/configuration#the-cursor).

## lys-login switch-user

```text
lys-login switch-user
```

Run in a session the display manager started: locks the running sessions and shows a
greeter on a free VT, where another user logs in beside them, or picks a user whose
session runs to switch back to it without a password. The lock screen's Switch User
button does the same; `login.lock.show-switch-user` turns it off. See
[Switching users](/reference/architecture#switching-users).

## Internal modes

`lys-login worker`, `lys-login daemon-resume` and `lys-login vault-unlock` are started by lys-login itself.

## Environment

| Variable | What it does |
| --- | --- |
| `LYS_UI_RENDERER` | `cpu` always draws on the CPU, `gpu` makes a GPU that cannot draw an error, and unset or `auto` picks. |
| `XCURSOR_THEME`, `XCURSOR_SIZE` | The cursor, last in the [order](/guide/configuration#the-cursor) the greeter resolves it in. |
