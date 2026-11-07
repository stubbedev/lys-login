package auth

import (
	"errors"
	"testing"
)

func TestAutologinTakesTheDiskPassphraseAsTheSecret(t *testing.T) {
	f := &fakePAM{permit: true, authtok: []byte("luks passphrase")}
	l, err := autologin(f, "lys-login-autologin", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if got := string(l.TakeSecret()); got != "luks passphrase" {
		t.Errorf("the secret is %q, want the passphrase pam_systemd_loadkey set", got)
	}
	if f.service != "lys-login-autologin" || f.user != "alice" {
		t.Errorf("transaction %s for %s", f.service, f.user)
	}
}

func TestAutologinWithoutAKeyHasNoSecret(t *testing.T) {
	l, err := autologin(&fakePAM{permit: true}, "lys-login-autologin", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if secret := l.TakeSecret(); secret != nil {
		t.Errorf("no key left a secret %q", secret)
	}
}

func TestAutologinRefusesAStackThatAsks(t *testing.T) {
	f := &fakePAM{password: "pw", batches: [][]pamMessage{{{style: pamPromptEchoOff, text: "Password: "}}}}
	_, err := autologin(f, "lys-login-autologin", "alice")
	if _, ok := errors.AsType[*RefusedError](err); !ok {
		t.Fatalf("a prompting stack: %v, want a refusal", err)
	}
}
