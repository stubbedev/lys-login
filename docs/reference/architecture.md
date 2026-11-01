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

The worker tries for up to a minute, so that a keyring started with the graphical
session or by D-Bus activation still gets it; the login never waits for this, and
without a keyring it gives up and logs why. The password is then cleared.

## Sockets and files

| Path | Who may use it | What for |
| --- | --- | --- |
| `/run/lys-login/greeter.sock` | The greeter's account (`0600`) | The greetd protocol. |
| `/run/lys-login/apply.sock` | Root and the user whose session runs | [`apply-config`](/guide/configuration#change-the-login-screen-from-your-session). |
| `/run/lys-login/cursor.sock` | The user whose session runs | [`record-cursor`](/guide/configuration#the-cursor). |
| `/run/lys-login/splash.sock` | Root and the greeter's account | The greeter's first frame, which quits lys-splash. |
| `/run/lys-login/kiosk.yaml` | Read by the greeter's account | The generated lys-wm config. |
| `/var/lib/lys-greeter/` | The greeter's account (`0700`) | The last session and username. |
| `/var/lib/lys-login/cursor/<uid>` | World-readable, written by the daemon | Each user's cursor record. |
| `/etc/lys-shell/config.yaml` | Read by the greeter | The system config. |
| `/etc/lys-shell/runtime.yaml` | Written by `apply-config` | The overlay on it. |

`apply.sock`, `cursor.sock` and `splash.sock` are open to every local user (`0666`).
The daemon checks the peer's uid on each request and answers a refusal with its reason.
