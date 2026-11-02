package greeter

import (
	"bytes"
	"log"
	"slices"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/dbustest"
	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
)

type fakeAccounts struct {
	system  map[string]bool
	block   chan struct{}
	unknown string
}

func (f *fakeAccounts) FindUserByName(name string) (dbus.ObjectPath, *dbus.Error) {
	if f.block != nil {
		<-f.block
	}
	if name == f.unknown {
		return "", dbus.NewError("org.freedesktop.Accounts.Error.Failed", []any{"no such user"})
	}
	return dbus.ObjectPath("/org/freedesktop/Accounts/" + name), nil
}

type fakeAccountsUser struct{ system bool }

func (f fakeAccountsUser) Get(iface, prop string) (dbus.Variant, *dbus.Error) {
	if iface != accountsUserIface || prop != "SystemAccount" {
		return dbus.Variant{}, dbus.NewError("org.freedesktop.DBus.Error.InvalidArgs", []any{iface + "." + prop})
	}
	return dbus.MakeVariant(f.system), nil
}

func serveAccounts(t *testing.T, f *fakeAccounts) {
	t.Helper()
	bus := dbustest.Start(t)
	bus.UseAsSystemBus(t)
	conn := bus.Conn(t)
	if err := conn.Export(f, accountsPath, accountsBusName); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ada", "bob", "temp_root"} {
		path := dbus.ObjectPath("/org/freedesktop/Accounts/" + name)
		if err := conn.Export(fakeAccountsUser{system: f.system[name]}, path, "org.freedesktop.DBus.Properties"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := conn.RequestName(accountsBusName, dbus.NameFlagDoNotQueue); err != nil {
		t.Fatal(err)
	}
}

func accountUsers() []User {
	return []User{{Name: "ada"}, {Name: "temp_root"}, {Name: "bob"}}
}

func names(users []User) []string {
	got := make([]string, 0, len(users))
	for _, u := range users {
		got = append(got, u.Name)
	}
	return got
}

func TestSystemAccountsAreLeftOutOfTheList(t *testing.T) {
	serveAccounts(t, &fakeAccounts{system: map[string]bool{"temp_root": true}})
	got := names(withoutSystemAccounts(accountUsers(), 5*time.Second))
	if want := []string{"ada", "bob"}; !slices.Equal(got, want) {
		t.Errorf("listed = %v, want %v", got, want)
	}
}

func TestEveryoneIsListedWhenNoAccountIsASystemAccount(t *testing.T) {
	serveAccounts(t, &fakeAccounts{})
	got := names(withoutSystemAccounts(accountUsers(), 5*time.Second))
	if want := names(accountUsers()); !slices.Equal(got, want) {
		t.Errorf("listed = %v, want %v", got, want)
	}
}

func TestEveryoneIsListedWithoutAccountsService(t *testing.T) {
	dbustest.Start(t).UseAsSystemBus(t)
	got := names(withoutSystemAccounts(accountUsers(), 5*time.Second))
	if want := names(accountUsers()); !slices.Equal(got, want) {
		t.Errorf("listed = %v, want %v", got, want)
	}
}

func TestEveryoneIsListedWithoutASystemBus(t *testing.T) {
	t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", "unix:path="+t.TempDir()+"/none")
	got := names(withoutSystemAccounts(accountUsers(), 5*time.Second))
	if want := names(accountUsers()); !slices.Equal(got, want) {
		t.Errorf("listed = %v, want %v", got, want)
	}
}

func TestAnAccountItCannotAnswerForStaysListed(t *testing.T) {
	serveAccounts(t, &fakeAccounts{system: map[string]bool{"temp_root": true}, unknown: "ada"})
	got := names(withoutSystemAccounts(accountUsers(), 5*time.Second))
	if want := []string{"ada", "bob"}; !slices.Equal(got, want) {
		t.Errorf("listed = %v, want %v", got, want)
	}
}

func TestAHungAccountsServiceIsBoundedAndHidesNobody(t *testing.T) {
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	serveAccounts(t, &fakeAccounts{system: map[string]bool{"temp_root": true}, block: block})
	var logs bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(prev) })
	begin := time.Now()
	got := names(withoutSystemAccounts(accountUsers(), 200*time.Millisecond))
	if d := time.Since(begin); d > 3*time.Second {
		t.Errorf("took %v, want it bounded by the timeout", d)
	}
	if want := names(accountUsers()); !slices.Equal(got, want) {
		t.Errorf("listed = %v, want %v", got, want)
	}
	if !bytes.Contains(logs.Bytes(), []byte("AccountsService did not answer")) {
		t.Errorf("log = %q, want the reason", logs.String())
	}
}
