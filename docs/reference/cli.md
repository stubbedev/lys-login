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
lys-login daemon [--vt N] [--service NAME] [--greeter-user NAME] [--compositor PATH] [--runtime-dir DIR] [--splash PATH] [-- <greet args...>]
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

The packaged unit runs:

```text
lys-login daemon --vt 1 --service lys-login --greeter-user lys-greeter --compositor /usr/bin/lys-wm --splash /usr/bin/lys-splash -- --config /etc/lys-shell/config.yaml --state /var/lib/lys-greeter/last-session
```

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

## Internal modes

`lys-login worker` and `lys-login vault-unlock` are started by lys-login itself.

## Environment

| Variable | What it does |
| --- | --- |
| `LYS_UI_RENDERER` | `cpu` always draws on the CPU, `gpu` makes a GPU that cannot draw an error, and unset or `auto` picks. |
| `XCURSOR_THEME`, `XCURSOR_SIZE` | The cursor, last in the [order](/guide/configuration#the-cursor) the greeter resolves it in. |
