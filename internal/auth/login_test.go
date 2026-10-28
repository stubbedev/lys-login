package auth

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestLoginOpensAndClosesItsSession(t *testing.T) {
	f := &fakePAM{batches: [][]pamMessage{{{style: pamPromptEchoOff, text: "Password:"}}}, password: "pw"}
	l, err := authenticate(f, "lys-login", "alice", &pamConversation{ask: answering("pw", &[]Prompt{})})
	if err != nil {
		t.Fatal(err)
	}
	env, err := l.OpenSession([]string{"XDG_SESSION_CLASS=user", "XDG_VTNR=1"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(env, "XDG_SESSION_CLASS=user") || !slices.Contains(env, "XDG_SESSION_ID=7") {
		t.Errorf("session env = %v, want the handed and the opened entries", env)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	want := "authenticate,acct_mgmt,setcred 0x2,open_session,close_session,setcred 0x4,end"
	if got := strings.Join(f.calls, ","); got != want {
		t.Errorf("calls = %s, want %s", got, want)
	}
	if f.service != "lys-login" || f.user != "alice" {
		t.Errorf("transaction for %s/%s", f.service, f.user)
	}
}

// The login hands over the password it authenticated with once, and
// clears what the conversation kept of it.
func TestLoginTakeSecret(t *testing.T) {
	f := &fakePAM{batches: [][]pamMessage{{{style: pamPromptEchoOff, text: "Password:"}}}, password: "pw"}
	conv := &pamConversation{ask: answering("pw", &[]Prompt{})}
	l, err := authenticate(f, "lys-login", "alice", conv)
	if err != nil {
		t.Fatal(err)
	}
	if conv.secret != nil {
		t.Errorf("the conversation still holds %q", conv.secret)
	}
	if got := l.TakeSecret(); string(got) != "pw" {
		t.Errorf("TakeSecret = %q, want the password", got)
	}
	if got := l.TakeSecret(); got != nil {
		t.Errorf("a second TakeSecret = %q, want nil", got)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	f = &fakePAM{batches: [][]pamMessage{{{style: pamPromptEchoOff, text: "Password:"}}}, password: "pw"}
	l, err = authenticate(f, "lys-login", "alice", &pamConversation{ask: answering("pw", &[]Prompt{})})
	if err != nil {
		t.Fatal(err)
	}
	kept := l.secret
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if string(kept) != "\x00\x00" || l.TakeSecret() != nil {
		t.Errorf("after Close the secret is %q, want it cleared", kept)
	}

	f = &fakePAM{batches: [][]pamMessage{{{style: pamPromptEchoOff, text: "Password:"}}}, password: "pw"}
	conv = &pamConversation{ask: answering("wrong", &[]Prompt{})}
	if _, err := authenticate(f, "lys-login", "alice", conv); err == nil {
		t.Fatal("a wrong password authenticated")
	}
	if string(conv.secret) != "\x00\x00\x00\x00\x00" {
		t.Errorf("a refused password is kept as %q, want it cleared", conv.secret)
	}
}

func TestLoginFailuresEndTheTransaction(t *testing.T) {
	f := &fakePAM{batches: [][]pamMessage{{{style: pamPromptEchoOff, text: "Password:"}}}, password: "pw"}
	_, err := authenticate(f, "lys-login", "alice", &pamConversation{ask: answering("wrong", &[]Prompt{})})
	if refused, ok := errors.AsType[*RefusedError](err); !ok || refused.Step != "authentication" || refused.Reason != "Authentication failure" {
		t.Errorf("wrong password = %v, want PAM's refusal of the authentication", err)
	}
	if got := strings.Join(f.calls, ","); got != "authenticate,end" || f.endStatus != fakeAuthErr {
		t.Errorf("calls = %s, end status %d", got, f.endStatus)
	}

	f = &fakePAM{batches: [][]pamMessage{{{style: pamPromptEchoOff, text: "Password:"}}}, password: "pw", acctRC: fakeAcctErr}
	_, err = authenticate(f, "lys-login", "alice", &pamConversation{ask: answering("pw", &[]Prompt{})})
	if refused, ok := errors.AsType[*RefusedError](err); !ok || refused.Step != "account check" || refused.Reason != "User account has expired" {
		t.Errorf("expired account = %v, want PAM's refusal of the account", err)
	}

	f = &fakePAM{batches: [][]pamMessage{{{style: pamPromptEchoOff, text: "Password:"}}}, password: "pw", openRC: 14}
	l, err := authenticate(f, "lys-login", "alice", &pamConversation{ask: answering("pw", &[]Prompt{})})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.OpenSession(nil); err == nil {
		t.Fatal("a session PAM refused opened")
	}
	_ = l.Close()
	if got := strings.Join(f.calls, ","); got != "authenticate,acct_mgmt,setcred 0x2,open_session,setcred 0x4,end" || f.endStatus != 14 {
		t.Errorf("calls = %s, end status %d; want the credentials deleted and no close_session", got, f.endStatus)
	}
	if _, err := l.OpenSession([]string{"NOEQUALS"}); err == nil {
		t.Error("an environment entry without = was handed to PAM")
	}
}
