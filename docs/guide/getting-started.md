# Getting started

lys-login is one binary with these modes:

| Command | What it is |
| --- | --- |
| `lys-login daemon` | The display manager, a root service on a VT. |
| `lys-login greet` | The greeter, under `lys-login daemon` or under greetd. |
| `lys-login lock` | The session lock, started by lys-shell. |
| `lys-login upgrade` | Moves the running daemon to a new build in place. |
| `lys-login apply-config` | Changes the login screen from your session. |
| `lys-login record-cursor` | Hands your session's cursor to the daemon for the next greeter. |
| `lys-login switch-user` | Locks the running sessions and shows a greeter for another user. |

[Command line](/reference/cli) lists every flag.

## Install

| System | Page |
| --- | --- |
| NixOS | [NixOS](/install/nixos) |
| Debian, Ubuntu, Fedora | [Debian, Ubuntu and Fedora](/install/packages) |
| Anything else, or under greetd | [Other systems](/install/other) |

Releases and the flake cover x86_64 and aarch64.

## Requirements

- **libpam**, loaded at runtime.
- **lys-wm** for the display manager.
- **A session file**: a `*.desktop` file in `wayland-sessions` or `xsessions`.
  Without one, `daemon` and `greet` refuse to start and name the directories
  searched. X11 sessions start through `startx`, which must be on the session's
  PATH.
- **A compositor with `ext-session-lock-v1`** for the lock screen.
- Optional: **lys-secrets** or **gnome-keyring** to open with the login;
  **libvulkan** for GPU drawing (the CPU is used otherwise); **AccountsService**
  to hide system accounts from the user list.

## The greeter

- **Session picker**: Wayland and X11 sessions sorted by name, X11 ones labelled
  `(X11)`. The last one used is preselected. Hidden when there is only one.
- **User list**: accounts with UID 1000–59999 and a login shell, minus those
  AccountsService marks `SystemAccount=true` (2 s timeout). Avatars come from
  `/var/lib/AccountsService/icons/<user>` or `~/.face`. Hidden above 8 users;
  typing a name always works.
- **Remembered username**, prefilled with focus on the password.
- **PAM conversation**: extra prompts (one-time codes, expired password changes)
  and refusal reasons appear on screen. A wrong password keeps the greeter up.
- **Caps Lock warning.**
- **Keyboard layout chip** when the system has more than one layout; see
  [Configuration](/guide/configuration#keyboard-layout).
- **Accessibility menu**; see [Configuration](/guide/configuration#accessibility).
- **Power buttons**: `systemctl suspend`, `reboot` and `poweroff`.
- **Translations**: en-US and French, from lys-shell.

## Troubleshooting

The daemon, its greeters and its workers log to one unit:

```sh
journalctl -b -u lys-login
```

A greeter that fails is started again after 2 seconds. A greeter that fails
within 30 seconds of its start waits twice as long each time (2, 4, 8, 16
seconds); after 5 such failures in a row it is started every 5 minutes, and the
VT shows why, with the last lines the greeter and its compositor wrote. A
greeter that runs cleanly resets the count. The unit restarts if the daemon
exits.

When a session ends, the daemon removes the X display locks (`/tmp/.X<n>-lock`)
and sockets (`/tmp/.X11-unix/X<n>`) it left behind: only those owned by the
session's user whose lock names a process that no longer runs, so the next
greeter or session can take the display. To test a PAM service without logging out, see
[PAM services](/guide/pam#test-a-service).
