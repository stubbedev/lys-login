# The lock screen

`lys-login lock` locks the session with `ext-session-lock-v1`, one surface per
output, and exits on unlock. If the process dies the compositor keeps the session
locked. lys-shell runs `lys-login` from the PATH; the unlock needs a
[PAM service](/guide/pam#the-lock-screen-s-service).

## Triggers

lys-shell starts the lock for:

- logind's Lock signal (`loginctl lock-session`, idle daemons);
- `lys-shell lock`;
- `login.lock.lock-on-start`;
- a lock already held when the shell starts.

The lock is its own process, so it survives a shell restart; lys-shell restarts it
if it dies while the session is locked. It reports state to logind with
`SetLockedHint` and unlocks by password or on logind's Unlock signal.

## What it shows

- Clock and date.
- The current MPRIS track with play/pause and next. Only local cover art is drawn.
- Battery charge from UPower, on machines with a battery.
- The keyboard layout chip when the keymap has several layouts; click it or use the
  compositor's layout keys to switch.
- **Switch User**, under lys-login's display manager when another account
  exists. It leaves this session locked and shows a greeter on another VT; see
  [Switching users](/reference/architecture#switching-users).
- The failed-attempt count.

## Fingerprint

With fprintd and an enrolled finger, the lock verifies a fingerprint alongside the
password prompt; a match unlocks. Typing a password stops the reader. A
non-matching finger counts as a failed attempt. A fingerprint unlock opens no
keyring.

## Keyring and vault

The verified password opens the login collection of lys-secrets, else a running
gnome-keyring. Under autologin this is where the keyring is first opened.

With `bitwarden.unlock-with-session` (default off) the password is also tried
once per unlock on the Bitwarden vault, through a helper process that reads it
from a pipe.

## Configuration

`login.lock`, plus the two lock-only `login` keys:

| Key | Type | Default | Effect |
| --- | --- | --- | --- |
| `login.lock.enabled` | bool | `true` | Off makes `lys-login lock` exit 4 without locking. |
| `login.lock.lock-on-start` | bool | `false` | lys-shell locks as the session starts. |
| `login.lock.grace-period-ms` | uint | `0` | Pressing Enter within this time after locking unlocks without a password. |
| `login.lock.blank-timeout-ms` | uint | `0` | Blank the lock screen after this idle time; any key restores it. `0` never blanks. |
| `login.lock.pam-service` | string | `system-auth` | PAM service for the unlock. |
| `login.lock.show-media` | bool | `true` | Show the media track. |
| `login.lock.show-battery` | bool | `true` | Show the battery. |
| `login.lock.fingerprint` | bool | `true` | Use fprintd. |
| `login.lock.show-switch-user` | bool | `true` | Offer Switch User. |
| `login.max-attempts` | uint | `0` | Block input after this many failures; the session stays locked. `0` is unlimited. |
| `login.show-failed-attempts` | bool | `true` | Show the failure count. |

## Interface with lys-shell

`lys-login lock` takes no arguments. lys-shell's `shell/lockclient` is the other
side; `cmd/lys-login/lock_test.go` tests the contract against the binary.

| Signal | Meaning |
| --- | --- |
| `locked` on stdout | The compositor confirmed the lock. |
| Exit 0 | Unlocked. |
| Exit 1 | Other failure. |
| Exit 2 | Given an argument. |
| Exit 3 | Another lys-login already holds this session locked. |
| Exit 4 | The lock never took: `login.lock.enabled` is off, or the compositor lacks or refused the protocol. |

During a [display manager upgrade](/reference/architecture#upgrading-in-place) a
lock screen that is up keeps running on its old build until it unlocks.
