package greeter

import (
	"context"
	"errors"
	"log"
	"time"

	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
)

const (
	accountsBusName      = "org.freedesktop.Accounts"
	accountsPath         = dbus.ObjectPath("/org/freedesktop/Accounts")
	accountsUserIface    = "org.freedesktop.Accounts.User"
	accountsBusTimeout   = 2 * time.Second
	dbusServiceUnknown   = "org.freedesktop.DBus.Error.ServiceUnknown"
	dbusPropertiesGetter = "org.freedesktop.DBus.Properties.Get"
)

// withoutSystemAccounts leaves out the accounts AccountsService marks as
// system accounts: on Ubuntu an administrator's spare account has a login
// shell and a regular UID, and only this tells it from a person's. It decides
// the list alone: a left-out account still logs in by typing its name, and
// any doubt lists the account, as the list is a convenience and a stale one
// costs nothing where a wrongly short one would hide a person.
func withoutSystemAccounts(users []User, timeout time.Duration) []User {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	conn, err := dbus.ConnectSystemBus(dbus.WithContext(ctx))
	if err != nil {
		log.Printf("greeter: listing every login user: no system bus: %v", err)
		return users
	}
	defer func() {
		if err := conn.Close(); err != nil {
			log.Printf("greeter: close the system bus: %v", err)
		}
	}()
	listed := make([]User, 0, len(users))
	for i, u := range users {
		system, err := isSystemAccount(ctx, conn, u.Name)
		switch {
		case err == nil && system:
			log.Printf("greeter: not listing %s: AccountsService marks it a system account", u.Name)
			continue
		case err == nil:
		case isServiceUnknown(err) || ctx.Err() != nil:
			log.Printf("greeter: listing every login user: AccountsService did not answer: %v", err)
			return append(listed, users[i:]...)
		default:
			log.Printf("greeter: listing %s: ask AccountsService: %v", u.Name, err)
		}
		listed = append(listed, u)
	}
	return listed
}

func isSystemAccount(ctx context.Context, conn *dbus.Conn, name string) (bool, error) {
	var path dbus.ObjectPath
	if err := conn.Object(accountsBusName, accountsPath).CallWithContext(ctx, accountsBusName+".FindUserByName", 0, name).Store(&path); err != nil {
		return false, err
	}
	var system bool
	if err := conn.Object(accountsBusName, path).CallWithContext(ctx, dbusPropertiesGetter, 0, accountsUserIface, "SystemAccount").Store(&system); err != nil {
		return false, err
	}
	return system, nil
}

func isServiceUnknown(err error) bool {
	var busErr dbus.Error
	return errors.As(err, &busErr) && busErr.Name == dbusServiceUnknown
}
