package auth

import (
	"errors"
	"fmt"
	"log"
	"runtime"
	"slices"
)

// PAM return codes the backend uses (security/_pam_types.h).
const (
	pamSuccess = 0
	pamBufErr  = 5
	pamConvErr = 19
)

// PAM message styles (security/_pam_types.h).
const (
	pamPromptEchoOff = 1
	pamPromptEchoOn  = 2
	pamErrorMsg      = 3
	pamTextInfo      = 4
)

// pamMessage is one conversation message, decoded from C.
type pamMessage struct {
	style int32
	text  string
}

// pamReply is one conversation answer. set false leaves the C
// response slot NULL (info and error messages).
type pamReply struct {
	text string
	set  bool
}

// pamLib is the libpam surface the backend drives, narrowed so tests
// can substitute a scripted PAM. The production implementation
// (pam_purego.go) binds libpam with purego — no cgo.
type pamLib interface {
	// start opens a transaction for service and user whose
	// conversation calls converse for every batch of messages.
	start(service, user string, converse func([]pamMessage) ([]pamReply, int32)) (pamTxn, error)
}

// pamTxn is one open PAM transaction.
type pamTxn interface {
	authenticate() int32
	acctMgmt() int32
	setcred(flags int32) int32
	openSession() int32
	closeSession() int32
	putenv(kv string) int32
	getenvlist() []string
	end(status int32)
	strerror(code int32) string
}

// pam_setcred flags (security/_pam_types.h).
const (
	pamEstablishCred = 0x0002
	pamDeleteCred    = 0x0004
)

// PAM authenticates against a PAM service (e.g. system-auth): the
// in-session backend of the lock screen. The conversation runs on the
// worker goroutine Spawn creates, locked to its OS thread for the
// transaction's lifetime, so the UI loop never blocks on PAM. The
// password is never logged.
type PAM struct {
	// Service is the PAM service name (/etc/pam.d/<service>).
	Service string
	lib     pamLib
	// onVerified receive the verified password in turn, each borrowing
	// it until it returns; a field so tests observe them without a
	// daemon.
	onVerified []func(password []byte)
}

// NewPAM returns the backend for service over the system libpam. The
// verified password goes to each of verified in turn, then unlocks the
// session keyring; each borrows it only until it returns.
func NewPAM(service string, verified ...func(password []byte)) *PAM {
	return &PAM{Service: service, lib: systemPAM{}, onVerified: append(slices.Clip(verified), UnlockLoginKeyring)}
}

// Run implements Conversation. The transaction opens for username (the
// session user when empty), authenticates, then checks the account
// (pam_authenticate + pam_acct_mgmt), and always ends with pam_end
// carrying the last call's status, as nonstick's transaction does; it
// sets no credentials, an unlock needs none. On success the last secret
// answered unlocks the session keyring's login collection (lys-secrets,
// else gnome-keyring), best effort.
func (p *PAM) Run(username string, ask Ask) error {
	if username == "" {
		username = CurrentUsername()
	}
	// PAM modules keep per-thread state and the conversation callback
	// fires on the calling thread: pin the goroutine for the whole
	// transaction.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	conv := &pamConversation{ask: ask}
	l, err := authenticate(p.lib, p.Service, username, conv)
	if err != nil {
		log.Printf("auth: PAM %s: %v", p.Service, err)
		return err
	}
	secret := l.TakeSecret()
	defer clear(secret)
	if err := l.Close(); err != nil {
		log.Printf("auth: PAM %s: %v", p.Service, err)
	}
	if secret != nil {
		for _, fn := range p.onVerified {
			fn(secret)
		}
	}
	return nil
}

// pamConversation bridges PAM's conversation callback to an Ask.
type pamConversation struct {
	ask Ask
	// secret is the last echo-off answer handed to PAM, kept for the
	// post-auth keyring unlock.
	secret []byte
	// err records why the conversation aborted, for tests.
	err error
}

// errUnsupportedStyle aborts a conversation on a message style the
// backend cannot answer (Linux-PAM's binary prompts).
var errUnsupportedStyle = errors.New("unsupported PAM message style")

// handle answers one batch of messages, the pure half of the C
// callback: echo-off and echo-on prompts ask the UI (a cancelled
// prompt aborts with PAM_CONV_ERR), text-info and error messages are
// shown and answered with NULL. Any failure discards the whole batch,
// as the callback contract requires.
func (c *pamConversation) handle(msgs []pamMessage) ([]pamReply, int32) {
	replies := make([]pamReply, len(msgs))
	for i, m := range msgs {
		switch m.style {
		case pamPromptEchoOff, pamPromptEchoOn:
			kind := PromptSecret
			if m.style == pamPromptEchoOn {
				kind = PromptVisible
			}
			answer, ok := c.ask(Prompt{Kind: kind, Text: m.text})
			if !ok {
				c.err = ErrCancelled
				return nil, pamConvErr
			}
			if kind == PromptSecret {
				clear(c.secret)
				c.secret = []byte(answer)
			}
			replies[i] = pamReply{text: answer, set: true}
		case pamTextInfo:
			c.ask(Prompt{Kind: PromptInfo, Text: m.text})
		case pamErrorMsg:
			c.ask(Prompt{Kind: PromptError, Text: m.text})
		default:
			c.err = fmt.Errorf("%w %d", errUnsupportedStyle, m.style)
			return nil, pamConvErr
		}
	}
	return replies, pamSuccess
}
