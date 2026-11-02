package auth

import (
	"errors"
	"testing"
)

func TestRefusalsOfPAMCodes(t *testing.T) {
	for code, want := range map[string]Refusal{
		"AUTH_ERR":          RefusalCredentials,
		"USER_UNKNOWN":      RefusalCredentials,
		"ACCT_EXPIRED":      RefusalExpired,
		"CRED_EXPIRED":      RefusalExpired,
		"NEW_AUTHTOK_REQD":  RefusalExpired,
		"MAXTRIES":          RefusalTooManyAttempts,
		"PERM_DENIED":       RefusalDenied,
		"CRED_INSUFFICIENT": RefusalDenied,
		"SERVICE_ERR":       RefusalUnknown,
		"NEVER_HEARD_OF":    RefusalUnknown,
	} {
		if got := (&RefusedError{Call: "pam_authenticate", Code: code}).Refusal(); got != want {
			t.Errorf("%s = %d, want %d", code, got, want)
		}
	}
}

func TestRefusalsAreNotWrongPasswordsByDefault(t *testing.T) {
	var unset Refusal
	if unset != RefusalUnknown {
		t.Errorf("the zero refusal is %d, want unknown", unset)
	}
}

func TestParseRefusedRoundTrips(t *testing.T) {
	want := &RefusedError{Call: "pam_acct_mgmt", Code: "ACCT_EXPIRED"}
	got, ok := parseRefused(want.Error())
	if !ok || *got != *want {
		t.Errorf("parse(%q) = %+v, %v", want.Error(), got, ok)
	}
}

func TestParseRefusedRejectsOtherText(t *testing.T) {
	for _, text := range []string{"", "no session", "start PAM transaction login for alice: nope", "pam_authenticate", "pam_authenticate: "} {
		if got, ok := parseRefused(text); ok {
			t.Errorf("parse(%q) = %+v, want no refusal", text, got)
		}
	}
}

func TestSpawnCarriesTheRefusal(t *testing.T) {
	got := collect(t, &scripted{err: errors.Join(&RefusedError{Call: "pam_authenticate", Code: "AUTH_ERR"}, nil)}, nil)
	if len(got) != 1 || got[0].Refusal != RefusalCredentials || got[0].Reason != "pam_authenticate: AUTH_ERR" {
		t.Errorf("events = %+v", got)
	}
	got = collect(t, &scripted{err: errors.New("the socket closed")}, nil)
	if len(got) != 1 || got[0].Refusal != RefusalUnknown {
		t.Errorf("a plain error = %+v, want it unclassified", got)
	}
}
