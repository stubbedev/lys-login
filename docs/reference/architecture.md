# Architecture

```text
lys-login daemon → worker (greeter session) → lys-wm kiosk → lys-login greet
                 → worker (your login)      → your compositor
```

The daemon never authenticates or opens a session itself. Each greeter and each
login runs in a worker (`lys-login worker`, root) that holds the PAM transaction
for its lifetime and leads the logind session `pam_systemd` registers. A worker
stays on one OS thread, as PAM keeps per-thread state.

## Processes

| Process | Runs as | Job |
| --- | --- | --- |
| `lys-login daemon` | root | Owns the VTs and sockets, starts workers, keeps one session per user on its own VT, and a greeter when asked for or when no session runs. |
| `lys-login worker greeter` | root | Opens the greeter account's session and runs the compositor in it. |
| `lys-wm` (kiosk config) | `lys-greeter` | Shows the greeter, with no binds or borders. |
| `lys-login greet --hand-over` | `lys-greeter` | The login screen; speaks the greetd protocol to the daemon. |
| `lys-login worker session` | root, one per attempt | Authenticates, opens and runs the session, stops it on SIGTERM. Holds `session.<vt>.lock` in the runtime directory while running, so a restarted daemon waits for it instead of starting a greeter on top. |
| Your compositor | you | The session. |

The greeter speaks the same protocol under greetd (on `GREETD_SOCK`); under the
daemon the socket is reachable only by the greeter account.

## A login

1. Before each greeter the daemon activates the VT, opens every DRM card it does
   not hold (without becoming DRM master), writes `kiosk.yaml` with the cursor,
   keyboard and accessibility settings, and starts the greeter's worker. The
   greeter tells the daemon on `splash.sock` when its first frame is on screen;
   one that is not on screen 30 s after it starts is logged as a warning
   (`journalctl -u lys-login -p warning`).
2. Each login attempt is a new connection. The daemon starts a session worker
   over a socket pair and relays PAM prompts and answers as JSON lines.
3. A wrong password ends that worker; the greeter shows PAM's reason.
4. On success the greeter saves the session and username, hides the prompt,
   waits for that frame to be presented (at most 2 s) and asks to start.
5. The daemon stops the greeter's worker: compositor first, then the process
   group, killed after 5 s. The held DRM cards keep the last greeter frame on
   screen until the session's compositor takes over.
6. The worker opens the PAM session and runs the command as the user, in its own
   session with the VT as controlling terminal, through `/bin/sh` sourcing
   `/etc/profile` and `~/.profile`. logind sees type `wayland` or `x11`, class
   `user`.
7. When the session ends the worker closes the PAM session. The daemon switches
   to another running session, or shows the greeter.

## Switching users

`lys-login switch-user` and the lock screen's Switch User ask the daemon on
`switch.sock` for a greeter. The daemon locks the running sessions through logind
(`LockSession`), finds a free VT (`VT_OPENQRY`), holds it so logind spawns no
getty, and starts a greeter there. A new login gets its own VT.

Choosing a user whose session runs authenticates nobody: the daemon stops the
greeter, frees its VT and activates that session (`ActivateSession`), whose lock
screen is the gate. When a session ends its VT is freed and the daemon switches
to the previously used session, or the greeter. Stopping the daemon ends every
session.

## The keyring

Once the session starts, its worker hands the login password to the keyring from
a thread running with the user's uid and groups: the lys-secrets control socket,
else gnome-keyring's (`$GNOME_KEYRING_CONTROL/control`, else
`$XDG_RUNTIME_DIR/keyring/control`).

If neither answers, the worker connects to the session bus
(`$DBUS_SESSION_BUS_ADDRESS`, else `$XDG_RUNTIME_DIR/bus`) and activates
`dev.stubbe.lys.Vault1`, then `org.freedesktop.secrets`, then `org.gnome.keyring`,
stopping at the first that starts or already runs. It keeps trying for up to a
minute without delaying the login, then clears the password. The password never
crosses D-Bus, argv or the environment.

A refusal or a missing keyring is reported to the daemon, which logs one line
with journald priority warning (`journalctl -u lys-login -p warning`; a plain
`warning:` line without systemd):

| Found | Outcome |
| --- | --- |
| lys-secrets, gnome-keyring | Unlocked, or the refusal logged. |
| Another Secret Service provider (KeePassXC, KWallet) | Warning naming it, unless its default collection is already unlocked. |
| KWallet without the Secret Service API | Warning, unless its wallet is open. |
| No provider | Warning with the activation error. |
| No session bus | Warning with the address tried. |

A session whose bus address only its own process tree knows (e.g.
`dbus-run-session`) cannot be reached; a keyring that session starts itself
still gets the password within the minute.

KWallet accepts no password from outside. Put `pam_kwallet` in the service's PAM
stack; the worker runs that stack and finds the wallet open.

## Upgrading in place

`lys-login upgrade`, run from a new build, asks the daemon on `upgrade.sock` to
become that build. The daemon `execve`s it: the pid, systemd's main pid and the
children stay.

| What | During an upgrade |
| --- | --- |
| Running sessions | Keep running. |
| Each session's worker | Stays on its build until the session ends (a PAM handle cannot cross an exec); the new daemon follows it. |
| A lock screen that is up | Keeps running its build until it unlocks, and still opens lys-secrets. |
| The next lock screen | The new build, once it is `lys-login` on the PATH. |
| The next greeter and login | The new build and arguments. |
| A greeter that is up | Replaced by the new build's. A login in progress gets 10 s to finish, then is cancelled. |
| VT and DRM cards | The held cards pass through the exec; the console never takes the screen back. |
| Sockets | Listening sockets pass through the exec; queued requests are answered. |

State goes over in a memfd: versioned JSON with the descriptors, each session's
worker, VT, user, logind session and channel, and unread channel data. A build
that does not know the version or a worker's protocol refuses it.

An upgrade can fail at three points, each leaving the old daemon running:

1. **Before the exec**: the new build runs `lys-login daemon-resume --check`
   (10 s limit), checking the state, arguments, greeter account, compositor and
   libpam. Nothing has changed yet.
2. **The exec**: the call returns an error and the daemon continues.
3. **After the exec**: a new build that cannot take over, or panics, re-executes
   the old program from a descriptor it was handed (valid even if the package
   manager replaced the file), which resumes and reports the reason.

The reply travels on the same connection through the exec, so exit 0 means the
new daemon is running. A new build that dies outright after passing its check
is restarted by systemd like any crash.

## Sockets and files

| Path | Access | Purpose |
| --- | --- | --- |
| `/run/lys-login/greeter.sock` | Greeter account (`0600`) | greetd protocol. |
| `/run/lys-login/apply.sock` | Root, users with a running session, others through polkit | [`apply-config`](/guide/configuration#change-the-login-screen-from-your-session). |
| `/run/lys-login/cursor.sock` | Users with a running session | [`record-cursor`](/guide/configuration#cursor). |
| `/run/lys-login/splash.sock` | Root, greeter account | Greeter's first frame: quits lys-splash, and tells the daemon the greeter is on screen. |
| `/run/lys-login/upgrade.sock` | Root | [`upgrade`](/reference/cli#lys-login-upgrade). |
| `/run/lys-login/switch.sock` | Root, users with a running session | [`switch-user`](/reference/cli#lys-login-switch-user). |
| `/run/lys-login/kiosk.yaml` | Read by the greeter account | Generated lys-wm config. |
| `/run/lys-login/autologin.done` | Root | Marks the boot's autologin as done. |
| `/var/lib/lys-greeter/` | Greeter account (`0700`) | [Greeter state](/guide/configuration#state). |
| `/var/lib/lys-login/cursor/<uid>` | World-readable, written by the daemon | Cursor records. |
| `/etc/lys-shell/config.yaml` | Read by the greeter | System config. |
| `/etc/lys-shell/runtime.yaml` | Written by `apply-config` | Overlay on it. |

All sockets except `greeter.sock` are mode `0666`; the daemon checks the peer's
uid on every request and replies to a refusal with its reason.
