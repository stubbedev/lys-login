# How it fits together

```text
lys-login daemon → worker (greeter session) → lys-wm kiosk → lys-login greet
                 → worker (your login)      → your compositor
```

The daemon runs as root on a VT and never authenticates anyone or opens a session
itself. Each greeter and each login runs in a worker, a root process of its own
started as `lys-login worker`, which holds the PAM transaction from start to end and
leads the logind session `pam_systemd` registers. PAM keeps per-thread state, so a
worker stays on one OS thread for its whole life.

## The processes

| Process | Runs as | Job |
| --- | --- | --- |
| `lys-login daemon` | root | Owns the VT and the sockets, starts the workers, loops from greeter to session and back. |
| `lys-login worker greeter` | root | Opens the greeter account's session and runs the compositor in it. |
| `lys-wm`, in a kiosk config | `lys-greeter` | Shows the greeter. No binds, so keys reach only the greeter, and no borders. |
| `lys-login greet --hand-over` | `lys-greeter` | The login screen. Speaks the greetd protocol to the daemon. |
| `lys-login worker session` | root, one per login attempt | Authenticates, then opens the session and runs it. |
| Your compositor | you | The session. |

The greeter uses the same protocol under greetd, on the socket `GREETD_SOCK` names, so
it is the same greeter in both setups. With the daemon that socket is a private one
only the greeter's account can reach.

## A login

1. Before each greeter, the daemon activates the VT and opens every DRM card it does
   not yet hold, without being DRM master. It writes `kiosk.yaml` with the
   [cursor](/guide/configuration#the-cursor) for that greeter and starts the greeter's
   worker.
2. The greeter opens one connection per attempt. For each, the daemon starts a
   session worker over a socket pair and asks it to authenticate the user. The two
   exchange JSON lines, and the daemon relays PAM's prompts to the greeter and its
   answers back.
3. A wrong password ends that worker. The greeter shows PAM's reason and stays up.
4. On success the greeter saves the session and the username, hides the login card and
   the power buttons, waits until that frame is presented (2 seconds at most), and asks
   for the session.
5. The daemon stops the greeter's worker. It ends the compositor first, then the rest of
   the process group, and kills them after 5 seconds. The held DRM cards keep the kernel
   from putting the console back, so the greeter's last frame stays on screen for the
   session's compositor.
6. The session worker opens the PAM session and runs the command as the user, in a
   session of its own with the VT as its controlling terminal, through
   `/bin/sh`, which sources `/etc/profile` and `~/.profile` first. Under that worker the
   session is registered with logind as `wayland`, class `user`.
7. When the session ends, the worker closes the PAM session and the greeter comes back.

## The keyring

Once the session starts, its worker hands the login password to the session's keyring,
from a thread that first takes your uid and groups, as `pam_gnome_keyring` does from
gdm's worker. It goes to the control socket of lys-secrets, else of gnome-keyring.

When neither control socket answers, the worker starts a keyring on the session's bus, at
`$DBUS_SESSION_BUS_ADDRESS`, else `$XDG_RUNTIME_DIR/bus`, retrying until that bus appears. It
asks D-Bus to activate `dev.stubbe.lys.Vault1`, which is lys-secrets, then
`org.freedesktop.secrets` and `org.gnome.keyring`, stopping at the first that starts or
already runs. A gnome-keyring behind a systemd socket unit needs no activation: connecting
to its socket starts it.

The worker tries for up to a minute, so that a keyring started with the graphical session
still gets it; the login never waits for this. The password is then cleared. It never crosses
D-Bus, and never appears in an argument list or the environment.

If the keyring refuses the password, or none took it, the worker tells the daemon over its
channel, and the daemon logs one line. A worker runs in the user's session scope, so its own
output is filed under that scope; the daemon's is what `journalctl -u lys-login` shows. Under
systemd the line carries journald's `<4>` priority prefix, so `journalctl -u lys-login -p warning`
finds it; on another init, where stderr is not the journal, it is a plain `warning:` line. It
says what was found, for example the program serving `org.freedesktop.secrets`:

| Found | Outcome |
| --- | --- |
| lys-secrets, gnome-keyring | Unlocked, or the refusal is logged. |
| Another Secret Service provider, such as KeePassXC or KWallet's | No warning if its default collection is already unlocked; otherwise a warning naming it. |
| KWallet without the Secret Service API | No warning if its wallet is open; otherwise a warning naming it. |
| No provider | A warning with what activation said. |
| No session bus | A warning with the address tried. |

Non-systemd sessions whose bus only the session's own process tree knows, such as one started
by `dbus-run-session`, have no address the worker can find. A keyring the session starts itself
still gets the password within the minute.

KWallet takes no password from outside: `kwalletd` and `ksecretd` read a password hash only at
startup, from `pam_kwallet`, and offer no call to open a wallet with one. Put `pam_kwallet` in
the service's PAM stack, as a KDE login does; the worker runs that stack, and then finds the
wallet open.

## Upgrading in place

`lys-login upgrade`, run from a new build, asks the running daemon on `upgrade.sock` to
become that build. The daemon replaces its program with `execve`: the pid stays, so
systemd's main pid and the daemon's children stay too. An upgrade never touches a session.

| What | During an upgrade |
| --- | --- |
| Your compositor and everything in the session | Keeps running. |
| The session's worker | Stays on its old build until the session ends, as a PAM handle cannot cross an exec. The new daemon follows it, and the worker closes the PAM session at logout as usual. |
| A lock screen that is up | Keeps running on its old build until it unlocks. The daemon has no part in the lock. |
| The password at that lock screen | Reaches lys-secrets as before: the lock screen talks to its control socket itself. |
| The next lock screen | The new build, once `lys-login` on the PATH is. |
| The next login and the next greeter, with their compositor | The new build and the new arguments. |
| A greeter that is up | Stopped, and the new build's greeter takes its place. A login under way gets 10 seconds to finish; after that it is cancelled and the user tries again. |
| The VT and the DRM cards | The held cards pass through the exec, so the kernel never takes the screen back. |
| The sockets | The listening sockets pass through the exec, so a request waiting in one still gets its answer. |

The state goes over in a memfd whose descriptor the new program is told on its command
line: JSON with a version, the descriptors, the session's worker and channel, and what the
old daemon had read from that channel and not yet handled. A new build that does not know
the version, or the protocol of a worker it would have to follow, refuses it.

An upgrade fails in one of three places, each leaving the old daemon running:

1. **Before the exec.** The new build runs once as `lys-login daemon-resume --check`, for
   10 seconds at most. It checks the state, its arguments, the greeter's account and
   compositor, and that it can load libpam. Nothing has changed yet, and a greeter that is up
   stays up.
2. **The exec itself.** The call returns an error, and the daemon carries on.
3. **After the exec.** A new build that cannot take what was handed over, or panics trying,
   executes the old program again from a descriptor it was handed, which works even when a
   package manager has replaced the file. The old daemon resumes with the same state and
   answers `lys-login upgrade` with the reason.

The client gets its answer on the same connection, which also passes through the exec, so
exit 0 means the new daemon has taken over.

A new build that dies outright after passing its check, without a panic Go can catch,
is the one case this does not cover: the service then restarts as it would after any
crash of the daemon. A greeter that fails to come up on the new build is retried every 2
seconds, as at boot.

## Sockets and files

| Path | Who may use it | What for |
| --- | --- | --- |
| `/run/lys-login/greeter.sock` | The greeter's account (`0600`) | The greetd protocol. |
| `/run/lys-login/apply.sock` | Root and the user whose session runs | [`apply-config`](/guide/configuration#change-the-login-screen-from-your-session). |
| `/run/lys-login/cursor.sock` | The user whose session runs | [`record-cursor`](/guide/configuration#the-cursor). |
| `/run/lys-login/splash.sock` | Root and the greeter's account | The greeter's first frame, which quits lys-splash. |
| `/run/lys-login/upgrade.sock` | Root | [`upgrade`](/reference/cli#lys-login-upgrade). |
| `/run/lys-login/kiosk.yaml` | Read by the greeter's account | The generated lys-wm config. |
| `/var/lib/lys-greeter/` | The greeter's account (`0700`) | The last session and username. |
| `/var/lib/lys-login/cursor/<uid>` | World-readable, written by the daemon | Each user's cursor record. |
| `/etc/lys-shell/config.yaml` | Read by the greeter | The system config. |
| `/etc/lys-shell/runtime.yaml` | Written by `apply-config` | The overlay on it. |

`apply.sock`, `cursor.sock`, `splash.sock` and `upgrade.sock` are open to every local user (`0666`).
The daemon checks the peer's uid on each request and answers a refusal with its reason.
