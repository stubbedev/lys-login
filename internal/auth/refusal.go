package auth

import (
	"strconv"
	"strings"
)

// Refusal is why a login was turned down, for a login screen to word.
// The zero value claims nothing, so a failure nobody classified never
// reads as a wrong password.
type Refusal uint8

// Refusals.
const (
	RefusalUnknown Refusal = iota
	// RefusalCredentials: a wrong password or an unknown user, one
	// refusal so a login screen does not reveal which accounts exist.
	RefusalCredentials
	// RefusalExpired: the account or its password expired.
	RefusalExpired
	// RefusalTooManyAttempts: a module gave up after repeated failures.
	RefusalTooManyAttempts
	// RefusalDenied: the account may not log in, whatever its password.
	RefusalDenied
)

// pamCodes names Linux-PAM's return codes as greetd's description does,
// from _pam_types.h, with what each refuses.
var pamCodes = []struct {
	code    int32
	name    string
	refusal Refusal
}{
	{3, "SERVICE_ERR", RefusalUnknown},
	{4, "SYSTEM_ERR", RefusalUnknown},
	{6, "PERM_DENIED", RefusalDenied},
	{7, "AUTH_ERR", RefusalCredentials},
	{8, "CRED_INSUFFICIENT", RefusalDenied},
	{9, "AUTHINFO_UNAVAIL", RefusalUnknown},
	{10, "USER_UNKNOWN", RefusalCredentials},
	{11, "MAXTRIES", RefusalTooManyAttempts},
	{12, "NEW_AUTHTOK_REQD", RefusalExpired},
	{13, "ACCT_EXPIRED", RefusalExpired},
	{16, "CRED_EXPIRED", RefusalExpired},
	{19, "CONV_ERR", RefusalUnknown},
	{20, "AUTHTOK_ERR", RefusalUnknown},
}

func pamCodeName(code int32) string {
	for _, c := range pamCodes {
		if c.code == code {
			return c.name
		}
	}
	return strconv.Itoa(int(code))
}

// RefusedError is PAM turning a login down: the call that refused it and
// the code it returned. Its text is what greetd's protocol carries.
type RefusedError struct {
	Call, Code string
}

func (e *RefusedError) Error() string { return e.Call + ": " + e.Code }

// Refusal classifies the code; a code outside pamCodes is unknown.
func (e *RefusedError) Refusal() Refusal {
	for _, c := range pamCodes {
		if c.name == e.Code {
			return c.refusal
		}
	}
	return RefusalUnknown
}

// parseRefused reads a greetd error description, "<pam call>: <code>",
// back into a RefusedError; false for any other text.
func parseRefused(description string) (*RefusedError, bool) {
	call, code, ok := strings.Cut(description, ": ")
	if !ok || !strings.HasPrefix(call, "pam_") || code == "" {
		return nil, false
	}
	return &RefusedError{Call: call, Code: code}, true
}
