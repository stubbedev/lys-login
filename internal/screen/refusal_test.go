package screen

import (
	"testing"

	"github.com/stubbedev/lys-desktop/i18n"

	"github.com/stubbedev/lys-login/internal/auth"
)

func TestRefusalTextWordsEachRefusal(t *testing.T) {
	for r, id := range map[auth.Refusal]string{
		auth.RefusalCredentials:     "greeter-login-incorrect",
		auth.RefusalExpired:         "greeter-login-expired",
		auth.RefusalTooManyAttempts: "greeter-login-too-many",
		auth.RefusalDenied:          "greeter-login-denied",
		auth.RefusalUnknown:         "greeter-login-failed",
	} {
		if got, want := RefusalText(r), i18n.Greeter().Get(id); got != want {
			t.Errorf("refusal %d = %q, want %q", r, got, want)
		}
	}
}

func TestExpiredAccountIsNotAWrongPassword(t *testing.T) {
	incorrect := RefusalText(auth.RefusalCredentials)
	for _, r := range []auth.Refusal{auth.RefusalExpired, auth.RefusalTooManyAttempts, auth.RefusalDenied, auth.RefusalUnknown} {
		if RefusalText(r) == incorrect {
			t.Errorf("refusal %d reads as a wrong password", r)
		}
	}
}
