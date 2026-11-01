# The lock screen

`lys-login lock` locks the session with `ext-session-lock-v1`, one surface per
output, and exits once it unlocks. The compositor keeps the session locked if the
process dies.

lys-shell runs `lys-login lock` from the PATH, so installing lys-login is enough
for the binary. It also needs a [PAM service](/guide/pam#the-lock-screen-s-service).

## What locks the session

lys-shell starts it for:

- logind's Lock signal, which is what `loginctl lock-session` and idle daemons
  send;
- `lys-shell lock`;
- `login.lock.lock-on-start`, for autologin setups;
- a lock found held when the shell starts.

The lock is a process of its own, so it outlives a shell restart, and lys-shell
starts it again if it dies with the session still locked.

The screen reports its state to logind with `SetLockedHint`. It unlocks by
password or on logind's Unlock signal.

## Failed attempts

`login.show-failed-attempts` shows the count. `login.max-attempts` blocks further
input once reached, and the session stays locked; `0` means no limit. These two
keys belong to the lock screen: the greeter ignores them.

## What an unlock opens

The verified password also opens the login collection of lys-secrets, else a
running gnome-keyring. Under autologin the lock screen is the first place the
password appears, so this is what opens the keyring there.

With `bitwarden.unlock-with-session`, off by default, it also tries the password
on the Bitwarden vault, once per unlock, through a helper process that receives it
on a pipe. That is only useful when the master password is the login password.

## The contract with lys-shell

`lys-login lock` takes no arguments. `shell/lockclient` in lys-shell holds the
other side, and `cmd/lys-login/lock_test.go` pins it against the real binary.

| Signal | Meaning |
| --- | --- |
| `locked` on stdout | The compositor confirmed the lock. |
| Exit 0 | The session unlocked. |
| Exit 1 | Any other failure. |
| Exit 2 | It was given an argument. |
| Exit 3 | Another lys-login already holds this session locked. |
| Exit 4 | The lock never took: `login.lock.enabled` is off, or the compositor lacks the protocol or refused. |
