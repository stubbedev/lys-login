package screen

import (
	"github.com/stubbedev/lys-desktop/i18n"

	"github.com/stubbedev/lys-login/internal/auth"
)

// RefusalText words a refused login for the person logging in.
func RefusalText(r auth.Refusal) string {
	id := "greeter-login-failed"
	switch r {
	case auth.RefusalCredentials:
		id = "greeter-login-incorrect"
	case auth.RefusalExpired:
		id = "greeter-login-expired"
	case auth.RefusalTooManyAttempts:
		id = "greeter-login-too-many"
	case auth.RefusalDenied:
		id = "greeter-login-denied"
	case auth.RefusalUnknown:
	}
	return i18n.Greeter().Get(id)
}
