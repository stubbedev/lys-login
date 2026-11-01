# Getting started

lys-login is the lys desktop's login screen: one screen in two modes, the
greeter and the lock screen, plus a display manager of its own.

| Command | What it is |
| --- | --- |
| `lys-login daemon` | The display manager: a root service on a VT. |
| `lys-login greet` | The greeter, under `lys-login daemon` or under greetd in a cage kiosk. |
| `lys-login lock` | The session lock on `ext-session-lock-v1`, which lys-shell starts. |
| `lys-login apply-config` | Changes the login screen the system shows, from your session. |
| `lys-login record-cursor` | Hands your session's cursor to the display manager for the next greeter. |

[Command line](/reference/cli) lists every flag.

## Install

| System | How | Page |
| --- | --- | --- |
| NixOS | Flake input and system module | [NixOS](/install/nixos) |
| Debian, Ubuntu | `.deb` from the release | [Debian and Ubuntu](/install/debian) |
| Fedora | `.rpm` from the release | [Fedora](/install/fedora) |
| Anything else | Release archive, or the greeter under greetd | [Other systems](/install/other) |

Releases carry x86_64 packages. The flake also builds for aarch64-linux.

## What you need

- **libpam.** The binary is pure Go and loads it at runtime.
- **lys-wm** for the display manager, which shows the greeter in a lys-wm
  kiosk. The NixOS module brings its own; the packages recommend it.
- **A session to log into:** a `*.desktop` file in `wayland-sessions` or
  `xsessions`, the same files sddm and gdm read. Without one
  `lys-login daemon` and `lys-login greet` refuse to start and name the
  directories they looked in. X11 entries start through `startx`, which must
  be on the session's PATH.
- **A compositor with `ext-session-lock-v1`** for the lock screen.
- **lys-secrets or gnome-keyring**, optional, for the keyring to open with the
  login.
- **libvulkan**, optional. The login screen draws on the GPU where it can and
  on the CPU otherwise.

## What the greeter offers

- **Session picker.** Wayland and X11 sessions in one list sorted by name, X11
  entries labelled `(X11)`. The last one used is pre-selected, and the picker
  hides when there is only one.
- **User list.** Accounts from `/etc/passwd` with a UID from 1000 to 59999 and
  a login shell, as avatars from AccountsService
  (`/var/lib/AccountsService/icons/<user>`) or `~/.face`. It hides above 8
  users; the username field always works.
- **Remembered username,** pre-filled with focus on the password field.
- **PAM's own words.** Prompts past the password, such as a one-time code or an
  expired password, appear on screen, and so does the reason for a refusal. A
  wrong password keeps the greeter up for the next attempt.
- **Caps Lock warning** under the password field.
- **Shutdown and reboot buttons,** which run `systemctl poweroff` and
  `systemctl reboot`.
- **Localized labels,** in en-US and French, through lys-shell's translations.

## When something is wrong

The display manager logs to the journal, with each greeter's and each login's
worker on the same unit:

```sh
journalctl -b -u lys-login
```

A greeter that fails to come up is retried every 2 seconds, and the unit itself
restarts if the daemon exits. To test a PAM service without logging out, see
[PAM services](/guide/pam#test-a-service).

## Next

- [Configuration](/guide/configuration): the look, the cursor, changing the
  login screen from your session.
- [PAM services](/guide/pam): which service does what.
- [The lock screen](/guide/lock-screen): triggers and the contract with
  lys-shell.
- [How it fits together](/reference/architecture): the daemon, the greeter and
  the workers.
