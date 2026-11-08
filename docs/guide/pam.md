# PAM services

lys-login has no authentication of its own. Both screens run a PAM conversation
and show whatever PAM asks.

| Service | Used by | PAM calls |
| --- | --- | --- |
| `lys-login` (`--service`) | Each login through the daemon | authenticate, account, credentials, open session |
| `lys-login` (`--service`) | The greeter's own session | credentials, open session |
| `lys-login-autologin` | [Autologin](#autologin) | as `lys-login`, authenticating with `pam_permit` |
| `login.lock.pam-service` | The lock screen | authenticate, account |

Under greetd, greetd authenticates with its own service.

The Debian/Ubuntu and Fedora packages install `lys-login`, `lys-login-autologin`
and `lys-shell` into `/etc/pam.d`, from
[`resources/pam/`](https://github.com/stubbedev/lys-login/tree/main/resources/pam).
The NixOS module declares them.

## The display manager's service

It needs a session stack with `pam_systemd`, which registers the greeter and each
login with logind. The workers set in the PAM environment:

| Variable | Value |
| --- | --- |
| `XDG_SESSION_TYPE` | `wayland` for the greeter and Wayland sessions, `x11` for X11 sessions; a custom session is `wayland` unless `--env` overrides it |
| `XDG_SESSION_CLASS` | `greeter` or `user` |
| `XDG_SEAT` | `seat0` |
| `XDG_VTNR` | The session's VT |

- **Debian, Ubuntu**: `common-auth`, `common-account`, `common-session`,
  `common-password`.
- **Fedora**: `password-auth` and `postlogin`.
- **NixOS**: `security.pam.services.lys-login.startSession = true`.

Elsewhere, adapt one of the packaged files.

## Autologin

`--autologin USER` logs USER in once per boot, before the first greeter, through
`lys-login-autologin`: `pam_systemd_loadkey` then `pam_permit`, with the account,
session and password stacks of the display manager. If it fails the greeter
shows. After USER logs out the greeter shows.

The worker then reads the `cryptsetup` key that systemd-cryptsetup left in the
kernel keyring at boot and hands it to the [keyring](#the-keyring) like a typed
password. The unit runs with `KeyringMode=inherit` to see that key. When the
disk passphrase is also the keyring password, one prompt at boot unlocks the disk,
logs in and opens the keyring.

With no key there (no LUKS, TPM unlock, initramfs-tools' cryptroot) or a
passphrase the keyring rejects, the worker logs it and the keyring stays locked
until the lock screen opens it. `login.lock.lock-on-start` locks the session as
it starts.

## The lock screen's service

The lock screen authenticates against `login.lock.pam-service`, default
`system-auth`. That service exists on Arch and Fedora but not on Debian or NixOS.

- **Debian, Ubuntu, Fedora**: the package installs `lys-shell` (over
  `common-auth`/`common-account`, or `system-auth`). Point the config at it:

  ```yaml
  login:
    lock:
      pam-service: lys-shell
  ```

  Fedora's default `system-auth` also works.
- **NixOS**: `programs.lys-login.lock.enable = true` declares the service named
  by `lock.pamService` (default `lys-shell`). Set the same name in the config.
  Its gnome-keyring lines are enabled unless `services.lys-secrets.enable` is set.
- **Elsewhere** (home-manager's `lock.enable` only prints these steps): name an
  existing service or create `/etc/pam.d/lys-shell`:

  ```text
  auth     required   pam_unix.so
  auth     optional   pam_gnome_keyring.so
  account  required   pam_unix.so
  password required   pam_unix.so
  password optional   pam_gnome_keyring.so use_authtok
  session  required   pam_unix.so
  session  optional   pam_gnome_keyring.so auto_start
  ```

  `pam_unix` must be `required`: `sufficient` skips the keyring lines. The
  keyring lines are `optional` and do nothing without gnome-keyring (Debian
  package `libpam-gnome-keyring`).

## The keyring

No PAM module is needed to open the keyring. After a successful authentication
the password goes to the login collection of lys-secrets, else gnome-keyring,
starting either on the session bus if needed. When PAM changes an expired
password, the lys-secrets collection is re-keyed to the new one. Failures are
logged (`journalctl -u lys-login -p warning` under the daemon) and never block the
login or unlock.

KWallet opens only through `pam_kwallet` in the PAM stack.
[Architecture](/reference/architecture#the-keyring) has the details.

## Test a service

[pamtester](https://pamtester.sourceforge.net/) runs a service without logging out:

```sh
pamtester lys-shell "$USER" authenticate
```
