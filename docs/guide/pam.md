# PAM services

lys-login has no authentication of its own: both screens run a PAM conversation
and show what PAM asks, so a one-time code or an expired password works wherever
the service asks for it.

| Service | Used by | Calls | Where it comes from |
| --- | --- | --- | --- |
| `lys-login` (`--service`) | The display manager, for each login | authenticate, account check, credentials, open session | Package, NixOS module |
| `lys-login` (`--service`) | The display manager, for the greeter's own session | credentials, open session; nothing to authenticate | Package, NixOS module |
| `login.lock.pam-service` | The lock screen | authenticate, account check | See [below](#the-lock-screen-s-service) |

Under greetd, greetd runs the login with its own service, and lys-login only
talks to it.

## The display manager's service

It needs a session stack with `pam_systemd`, which registers the greeter and each
login with logind on the seat and VT the daemon hands it. The workers put
`XDG_SESSION_TYPE` (`wayland` for the greeter and Wayland sessions, `x11` for
X11 sessions, `wayland` for a custom session unless `--env` says otherwise),
`XDG_SESSION_CLASS` (`greeter` or `user`),
`XDG_SEAT=seat0` and `XDG_VTNR` in the PAM environment for it.

- **Debian and Ubuntu:** the usual display-manager stack over `common-auth`,
  `common-account`, `common-session` and `common-password`, with `pam_systemd`
  from the common session
  ([`resources/pam/debian/lys-login`](https://github.com/stubbedev/lys-login/blob/main/resources/pam/debian/lys-login)).
- **Fedora:** over `password-auth` and `postlogin`
  ([`resources/pam/fedora/lys-login`](https://github.com/stubbedev/lys-login/blob/main/resources/pam/fedora/lys-login)).
- **NixOS:** `security.pam.services.lys-login.startSession = true`, set by the
  module.

Elsewhere, start from one of these and adjust the includes to your distribution.

## The lock screen's service

The lock screen authenticates against the service `login.lock.pam-service` names
in your lys-shell config. The config's default is `system-auth`, which exists on
Arch and Fedora but not on Debian or NixOS.

- **Debian, Ubuntu, Fedora:** the package installs a `lys-shell` service, over
  `common-auth` and `common-account` or `system-auth`. Point the config at it,
  the name the NixOS and home-manager modules use too:

  ```yaml
  login:
    lock:
      pam-service: lys-shell
  ```

- **NixOS:** `programs.lys-login.lock.enable = true` declares the service
  `lock.pamService` names, `lys-shell` by default. Set the same name in the
  config. Its gnome-keyring lines are on unless `services.lys-secrets.enable` is
  set.
- **Elsewhere:** home-manager cannot write `/etc/pam.d`, so its
  `programs.lys-login.lock.enable` only warns with these steps. Name an existing
  service, or create `/etc/pam.d/lys-shell`:

  ```text
  auth     required   pam_unix.so
  auth     optional   pam_gnome_keyring.so
  account  required   pam_unix.so
  password required   pam_unix.so
  password optional   pam_gnome_keyring.so use_authtok
  session  required   pam_unix.so
  session  optional   pam_gnome_keyring.so auto_start
  ```

  `pam_unix` must be `required`, not `sufficient`: a sufficient pass skips the
  keyring lines below it. Those lines are `optional`, so they do nothing without
  gnome-keyring. Debian and Ubuntu package the module as `libpam-gnome-keyring`.

## The keyring

Neither screen needs a PAM module to open your keyring. After a successful
authentication the password goes to the login collection of lys-secrets, else to
gnome-keyring, either started on the session's D-Bus when it is not running yet.
A failure there is logged and never blocks the login or the unlock. Under the
display manager it is a warning in `journalctl -u lys-login -p warning`. KWallet
is the exception: it opens only through `pam_kwallet` in the PAM stack. [How it fits together](/reference/architecture#the-keyring) has the
display manager's side of this.

## Test a service

[pamtester](https://pamtester.sourceforge.net/) runs a service without logging
out, which is how the distribution VM check tests `lys-shell`:

```sh
pamtester lys-shell "$USER" authenticate
```
