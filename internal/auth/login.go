package auth

import (
	"bytes"
	"fmt"
	"strings"
)

// Login is a display manager's PAM login: one transaction from the
// authentication through the session it opens, ended by Close. PAM
// keeps per-thread state, and pam_loginuid writes /proc/self/loginuid,
// which the kernel lets only the main thread write, so every call must
// come from the process's main thread; a session worker stays on it for
// its whole life.
type Login struct {
	txn  pamTxn
	user string
	// secret is the last secret the authentication was answered with,
	// until TakeSecret or Close.
	secret   []byte
	previous []byte
	// last is the status pam_end receives.
	last         int32
	credentialed bool
	opened       bool
}

// Authenticate starts service's transaction for user, authenticates it
// through ask and checks the account. A failure ends the transaction.
func Authenticate(service, user string, ask Ask) (*Login, error) {
	return authenticate(systemPAM{}, service, user, &pamConversation{ask: ask})
}

func Autologin(service, user string) (*Login, error) {
	return autologin(systemPAM{}, service, user)
}

func autologin(lib pamLib, service, user string) (*Login, error) {
	l, err := authenticate(lib, service, user, &pamConversation{ask: refuseAutologinPrompt})
	if err != nil {
		return nil, err
	}
	l.secret = l.txn.authtok()
	return l, nil
}

func refuseAutologinPrompt(Prompt) (string, bool) { return "", false }

// Greeter starts service's transaction for the greeter's user, who
// signs nothing in: only its session opens.
func Greeter(service, user string) (*Login, error) {
	txn, err := systemPAM{}.start(service, user, (&pamConversation{ask: func(Prompt) (string, bool) { return "", false }}).handle)
	if err != nil {
		return nil, fmt.Errorf("start PAM transaction %s for %s: %w", service, user, err)
	}
	return &Login{txn: txn, user: user, last: pamSuccess}, nil
}

func authenticate(lib pamLib, service, user string, conv *pamConversation) (*Login, error) {
	txn, err := lib.start(service, user, conv.handle)
	if err != nil {
		return nil, fmt.Errorf("start PAM transaction %s for %s: %w", service, user, err)
	}
	l := &Login{txn: txn, user: user}
	if l.last = txn.authenticate(); l.last != pamSuccess {
		err := &RefusedError{Call: "pam_authenticate", Code: pamCodeName(l.last)}
		clear(conv.secret)
		txn.end(l.last)
		return nil, err
	}
	l.last = txn.acctMgmt()
	if l.last == pamNewAuthtokReqd {
		previous := bytes.Clone(conv.secret)
		if l.last = txn.chauthtok(pamChangeExpiredAuthtok); l.last != pamSuccess {
			err := &RefusedError{Call: "pam_chauthtok", Code: pamCodeName(l.last)}
			clear(previous)
			clear(conv.secret)
			txn.end(l.last)
			return nil, err
		}
		l.previous = previous
	}
	if l.last != pamSuccess {
		err := &RefusedError{Call: "pam_acct_mgmt", Code: pamCodeName(l.last)}
		clear(conv.secret)
		txn.end(l.last)
		return nil, err
	}
	l.secret, conv.secret = conv.secret, nil
	return l, nil
}

// TakeSecret hands over the last secret the user authenticated with
// (the password, for a password login), nil without one, and forgets
// it: the caller clears it after use.
func (l *Login) TakeSecret() []byte {
	secret := l.secret
	l.secret = nil
	return secret
}

func (l *Login) HasSecret() bool { return l.secret != nil }

func (l *Login) TakePrevious() []byte {
	previous := l.previous
	l.previous = nil
	return previous
}

// User is the account the login is for.
func (l *Login) User() string { return l.user }

// OpenSession hands env (KEY=value: the session's type, class, seat and
// VT, which pam_systemd registers it with) to the transaction,
// establishes the credentials, opens the session, and returns the
// environment the session's process starts with.
func (l *Login) OpenSession(env []string) ([]string, error) {
	for _, kv := range env {
		if !strings.Contains(kv, "=") {
			return nil, fmt.Errorf("session environment entry %q is not KEY=value", kv)
		}
		if rc := l.txn.putenv(kv); rc != pamSuccess {
			l.last = rc
			return nil, fmt.Errorf("pam_putenv %s: %s", kv[:strings.IndexByte(kv, '=')], l.txn.strerror(rc))
		}
	}
	if l.last = l.txn.setcred(pamEstablishCred); l.last != pamSuccess {
		return nil, fmt.Errorf("establish credentials: %s", l.txn.strerror(l.last))
	}
	l.credentialed = true
	if l.last = l.txn.openSession(); l.last != pamSuccess {
		return nil, fmt.Errorf("open the session: %s", l.txn.strerror(l.last))
	}
	l.opened = true
	return l.txn.getenvlist(), nil
}

// Close closes an opened session, deletes the credentials it
// established, and ends the transaction.
func (l *Login) Close() error {
	clear(l.secret)
	l.secret = nil
	clear(l.previous)
	l.previous = nil
	var errs []string
	if l.opened {
		if rc := l.txn.closeSession(); rc != pamSuccess {
			l.last = rc
			errs = append(errs, "close the session: "+l.txn.strerror(rc))
		}
		l.opened = false
	}
	if l.credentialed {
		if rc := l.txn.setcred(pamDeleteCred); rc != pamSuccess {
			l.last = rc
			errs = append(errs, "delete credentials: "+l.txn.strerror(rc))
		}
		l.credentialed = false
	}
	l.txn.end(l.last)
	if len(errs) > 0 {
		return fmt.Errorf("end the PAM login: %s", strings.Join(errs, "; "))
	}
	return nil
}
