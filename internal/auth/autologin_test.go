package auth

import (
	"errors"
	"testing"
)

func diskKey(secret string) diskPassphraseFunc {
	return func() ([]byte, error) {
		if secret == "" {
			return nil, nil
		}
		return []byte(secret), nil
	}
}

func TestAutologinTakesTheDiskPassphraseAsTheSecret(t *testing.T) {
	f := &fakePAM{permit: true}
	l, err := autologin(f, "lys-login-autologin", "alice", diskKey("luks passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(l.TakeSecret()); got != "luks passphrase" {
		t.Errorf("the secret is %q, want the passphrase systemd-cryptsetup cached", got)
	}
	if f.service != "lys-login-autologin" || f.user != "alice" {
		t.Errorf("transaction %s for %s", f.service, f.user)
	}
}

func TestAutologinWithoutAKeyHasNoSecret(t *testing.T) {
	l, err := autologin(&fakePAM{permit: true}, "lys-login-autologin", "alice", diskKey(""))
	if err != nil {
		t.Fatal(err)
	}
	if secret := l.TakeSecret(); secret != nil {
		t.Errorf("no key left a secret %q", secret)
	}
}

func TestAutologinFailsWhenTheKeyringCannotBeRead(t *testing.T) {
	broken := errors.New("keyctl read: permission denied")
	_, err := autologin(&fakePAM{permit: true}, "lys-login-autologin", "alice", func() ([]byte, error) { return nil, broken })
	if !errors.Is(err, broken) {
		t.Fatalf("an unreadable keyring: %v, want it reported", err)
	}
}

func TestAutologinRefusesAStackThatAsks(t *testing.T) {
	f := &fakePAM{password: "pw", batches: [][]pamMessage{{{style: pamPromptEchoOff, text: "Password: "}}}}
	_, err := autologin(f, "lys-login-autologin", "alice", diskKey("unused"))
	if _, ok := errors.AsType[*RefusedError](err); !ok {
		t.Fatalf("a prompting stack: %v, want a refusal", err)
	}
}

func TestTheDiskKeyYieldsItsLastPassword(t *testing.T) {
	for _, c := range []struct{ payload, want string }{
		{"only", "only"},
		{"first\x00second", "second"},
		{"first\x00second\x00", "second"},
		{"", ""},
		{"\x00", ""},
	} {
		payload := []byte(c.payload)
		if got := string(lastPassword(payload)); got != c.want {
			t.Errorf("lastPassword(%q) = %q, want %q", c.payload, got, c.want)
		}
		for _, b := range payload {
			if b != 0 {
				t.Errorf("lastPassword(%q) left the payload uncleared: %q", c.payload, payload)
				break
			}
		}
	}
}
