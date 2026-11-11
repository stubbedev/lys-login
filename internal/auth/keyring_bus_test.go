package auth

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/sockdir"
	"github.com/stubbedev/lys-lib/testhome"
	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
	"github.com/stubbedev/lys-lib/xdg"
	"github.com/stubbedev/lys-secrets/control"
	"github.com/stubbedev/lys-secrets/vault"
)

const fakeKeyringArg = "fake-keyring"

func TestMain(m *testing.M) {
	if len(os.Args) > 3 && os.Args[1] == fakeKeyringArg {
		if err := fakeKeyringMain(os.Args[2], os.Args[3]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	testhome.Run(m)
}

func TestTheTestsReachNoHostSession(t *testing.T) { testhome.RequireNoHostSession(t) }

func fakeKeyringMain(kind, answer string) error {
	marks := os.Getenv("FAKE_KEYRING_MARKS")
	if err := os.WriteFile(filepath.Join(marks, kind+"-started"), nil, 0o600); err != nil {
		return err
	}
	conn, err := dbus.Connect(os.Getenv("DBUS_STARTER_ADDRESS"))
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer func() { _ = conn.Close() }()
	got := filepath.Join(marks, kind+"-got")
	switch kind {
	case "gnome":
		if err := serveGnomeControl(answer, got); err != nil {
			return err
		}
		return ownAndWait(conn, secretServiceName, gnomeKeyringName)
	case "lys-secrets":
		sock, err := control.SocketPath(xdg.OS)
		if err != nil {
			return err
		}
		ln, err := control.Listen(sock)
		if err != nil {
			return err
		}
		go control.Serve(context.Background(), ln, fileUnlocker(got))
		return ownAndWait(conn, vault.ServiceName, secretServiceName)
	}
	return fmt.Errorf("unknown fake keyring %q", kind)
}

type fileUnlocker string

func (f fileUnlocker) UnlockLogin(pw []byte) error {
	return os.WriteFile(string(f), pw, 0o600)
}

func (f fileUnlocker) RekeyLogin(_, next []byte) error {
	return os.WriteFile(string(f)+".rekeyed", next, 0o600)
}

func ownAndWait(conn *dbus.Conn, names ...string) error {
	for _, name := range names {
		if _, err := conn.RequestName(name, dbus.NameFlagDoNotQueue); err != nil {
			return fmt.Errorf("own %s: %w", name, err)
		}
	}
	select {
	case <-conn.Context().Done():
	case <-time.After(time.Minute):
	}
	return nil
}

func serveGnomeControl(answer, got string) error {
	result := uint32(keyringResultOK)
	if answer == "denied" {
		result = keyringDenied
	}
	runtime, ok := xdg.OS.RuntimeHome()
	if !ok {
		return errors.New("no XDG_RUNTIME_DIR")
	}
	dir := filepath.Join(runtime, "keyring")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	ln, err := net.Listen("unix", filepath.Join(dir, "control"))
	if err != nil {
		return err
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			head := make([]byte, 13)
			if _, err := io.ReadFull(conn, head); err == nil {
				arg := make([]byte, binary.BigEndian.Uint32(head[9:13]))
				if _, err := io.ReadFull(conn, arg); err == nil {
					_ = os.WriteFile(got, arg, 0o600)
				}
				var resp [8]byte
				binary.BigEndian.PutUint32(resp[0:4], keyringHeaderSize)
				binary.BigEndian.PutUint32(resp[4:8], result)
				_, _ = conn.Write(resp[:])
			}
			_ = conn.Close()
		}
	}()
	return nil
}

type fakeService struct {
	name, kind, answer string
}

type activatableBus struct {
	runtime, marks string
	env            xdg.Env
}

const activatableBusConfig = `<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-BUS Bus Configuration 1.0//EN"
 "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <type>session</type>
  <listen>unix:path=%s</listen>
  <auth>EXTERNAL</auth>
  <servicedir>%s</servicedir>
  <policy context="default">
    <allow send_destination="*"/>
    <allow eavesdrop="true"/>
    <allow own="*"/>
  </policy>
</busconfig>
`

func startActivatableBus(t *testing.T, services ...fakeService) *activatableBus {
	t.Helper()
	b := &activatableBus{runtime: sockdir.Dir(t), marks: t.TempDir()}
	b.env = sessionEnv(map[string]string{"XDG_RUNTIME_DIR": b.runtime})
	b.startDaemon(t, filepath.Join(b.runtime, "bus"), services...)
	return b
}

func (b *activatableBus) startDaemon(t *testing.T, sock string, services ...fakeService) {
	t.Helper()
	serviceDir := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range services {
		exec := fmt.Sprintf("%s %s %s %s", self, fakeKeyringArg, s.kind, s.answer)
		if s.kind == "" {
			exec = "/bin/false"
		}
		unit := fmt.Sprintf("[D-BUS Service]\nName=%s\nExec=%s\n", s.name, exec)
		if err := os.WriteFile(filepath.Join(serviceDir, s.name+".service"), []byte(unit), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	conf := filepath.Join(t.TempDir(), "bus.conf")
	if err := os.WriteFile(conf, fmt.Appendf(nil, activatableBusConfig, sock, serviceDir), 0o600); err != nil {
		t.Fatal(err)
	}
	daemon, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Fatalf("dbus-daemon: %v", err)
	}
	cmd := exec.Command(daemon, "--config-file="+conf, "--nofork", "--print-address=1")
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"XDG_RUNTIME_DIR=" + b.runtime,
		"FAKE_KEYRING_MARKS=" + b.marks,
	}
	cmd.Stderr = os.Stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_ = cmd.Wait()
	})
	ready := make(chan error, 1)
	go func() {
		_, err := bufio.NewReader(out).ReadString('\n')
		ready <- err
	}()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatalf("dbus-daemon printed no address: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("dbus-daemon printed no address within 10s")
	}
}

func (b *activatableBus) address() string {
	return "unix:path=" + filepath.Join(b.runtime, "bus")
}

func (b *activatableBus) conn(t *testing.T) *dbus.Conn {
	t.Helper()
	conn, err := dbus.Connect(b.address())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func (b *activatableBus) got(t *testing.T, kind string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(b.marks, kind+"-got"))
	if err != nil {
		t.Fatalf("%s got no password: %v", kind, err)
	}
	return string(data)
}

func (b *activatableBus) started(kind string) bool {
	_, err := os.Stat(filepath.Join(b.marks, kind+"-started"))
	return err == nil
}

func testDeadline(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), d)
	t.Cleanup(cancel)
	return ctx
}

func TestAwaitActivatesTheKeyringNoClientAskedFor(t *testing.T) {
	b := startActivatableBus(t, fakeService{secretServiceName, "gnome", "ok"})
	if err := AwaitLoginKeyring(testDeadline(t, 15*time.Second), b.env, []byte("pw"), nil); err != nil {
		t.Fatalf("AwaitLoginKeyring = %v", err)
	}
	if got := b.got(t, "gnome"); got != "pw" {
		t.Errorf("gnome-keyring got %q", got)
	}
}

func TestAwaitActivatesGnomeKeyringByItsOwnName(t *testing.T) {
	b := startActivatableBus(t, fakeService{gnomeKeyringName, "gnome", "ok"})
	if err := AwaitLoginKeyring(testDeadline(t, 15*time.Second), b.env, []byte("pw"), nil); err != nil {
		t.Fatalf("AwaitLoginKeyring = %v", err)
	}
	if got := b.got(t, "gnome"); got != "pw" {
		t.Errorf("gnome-keyring got %q", got)
	}
}

func TestAwaitActivatesOnTheNamedBus(t *testing.T) {
	b := &activatableBus{runtime: sockdir.Dir(t), marks: t.TempDir()}
	elsewhere := filepath.Join(sockdir.Dir(t), "elsewhere")
	b.startDaemon(t, elsewhere, fakeService{secretServiceName, "gnome", "ok"})
	env := sessionEnv(map[string]string{"XDG_RUNTIME_DIR": b.runtime, "DBUS_SESSION_BUS_ADDRESS": "unix:path=" + elsewhere})
	if err := AwaitLoginKeyring(testDeadline(t, 15*time.Second), env, []byte("pw"), nil); err != nil {
		t.Fatalf("AwaitLoginKeyring = %v", err)
	}
	if got := b.got(t, "gnome"); got != "pw" {
		t.Errorf("gnome-keyring got %q", got)
	}
}

func TestAwaitWaitsForASessionBusThatStartsLate(t *testing.T) {
	runtime := sockdir.Dir(t)
	b := &activatableBus{runtime: runtime, marks: t.TempDir(), env: sessionEnv(map[string]string{"XDG_RUNTIME_DIR": runtime})}
	errs := make(chan error, 1)
	go func() { errs <- AwaitLoginKeyring(testDeadline(t, 15*time.Second), b.env, []byte("pw"), nil) }()
	time.Sleep(3 * keyringPoll)
	select {
	case err := <-errs:
		t.Fatalf("returned before the bus ran: %v", err)
	default:
	}
	b.startDaemon(t, filepath.Join(runtime, "bus"), fakeService{secretServiceName, "gnome", "ok"})
	if err := <-errs; err != nil {
		t.Fatalf("AwaitLoginKeyring = %v", err)
	}
	if got := b.got(t, "gnome"); got != "pw" {
		t.Errorf("gnome-keyring got %q", got)
	}
}

func TestAwaitActivatesLysSecretsBeforeGnomeKeyring(t *testing.T) {
	b := startActivatableBus(t,
		fakeService{vault.ServiceName, "lys-secrets", "ok"},
		fakeService{secretServiceName, "gnome", "ok"},
		fakeService{gnomeKeyringName, "gnome", "ok"},
	)
	if err := AwaitLoginKeyring(testDeadline(t, 15*time.Second), b.env, []byte("pw"), nil); err != nil {
		t.Fatalf("AwaitLoginKeyring = %v", err)
	}
	if got := b.got(t, "lys-secrets"); got != "pw" {
		t.Errorf("lys-secrets got %q", got)
	}
	if b.started("gnome") {
		t.Error("gnome-keyring was activated though lys-secrets is installed")
	}
}

func TestAwaitReportsADeniedPasswordAtOnce(t *testing.T) {
	b := startActivatableBus(t, fakeService{secretServiceName, "gnome", "denied"})
	err := AwaitLoginKeyring(testDeadline(t, 15*time.Second), b.env, []byte("wrong"), nil)
	if err == nil || !strings.Contains(err.Error(), "denied") || errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a refused password = %v, want the denial before the deadline", err)
	}
}

func TestAwaitWithoutAnyKeyringSaysSo(t *testing.T) {
	b := startActivatableBus(t)
	err := AwaitLoginKeyring(testDeadline(t, 2*keyringPoll), b.env, []byte("pw"), nil)
	for _, want := range []error{errNoKeyring, context.DeadlineExceeded} {
		if !errors.Is(err, want) {
			t.Errorf("no keyring = %v, want %v", err, want)
		}
	}
	if err == nil || !strings.Contains(err.Error(), "no provider on the session bus") || !strings.Contains(err.Error(), secretServiceName) {
		t.Errorf("no keyring = %v, want what activation said about %s", err, secretServiceName)
	}
}

func TestAwaitWithoutASessionBusNamesIt(t *testing.T) {
	runtime := sockdir.Dir(t)
	err := AwaitLoginKeyring(testDeadline(t, 2*keyringPoll), sessionEnv(map[string]string{"XDG_RUNTIME_DIR": runtime}), []byte("pw"), nil)
	if !errors.Is(err, errNoKeyring) || !strings.Contains(err.Error(), "no session bus at unix:path="+runtime) {
		t.Errorf("no bus = %v, want the derived address named", err)
	}
}

func TestAwaitFailingActivationIsReported(t *testing.T) {
	b := startActivatableBus(t, fakeService{name: secretServiceName})
	err := AwaitLoginKeyring(testDeadline(t, 2*keyringPoll), b.env, []byte("pw"), nil)
	if !errors.Is(err, errNoKeyring) || !strings.Contains(err.Error(), secretServiceName+": ") {
		t.Errorf("a failing activation = %v, want its reason", err)
	}
}

type fakeCollection struct{ locked bool }

func (c fakeCollection) Get(iface, name string) (dbus.Variant, *dbus.Error) {
	if iface != collectionInterface || name != "Locked" {
		return dbus.Variant{}, dbus.NewError("org.freedesktop.DBus.Error.UnknownProperty", nil)
	}
	return dbus.MakeVariant(c.locked), nil
}

type fakeSecretService struct{ collection dbus.ObjectPath }

func (s fakeSecretService) ReadAlias(name string) (dbus.ObjectPath, *dbus.Error) {
	if name != "default" {
		return noObject, nil
	}
	return s.collection, nil
}

func ownCollection(t *testing.T, b *activatableBus, locked bool) {
	t.Helper()
	conn := b.conn(t)
	collection := dbus.ObjectPath("/org/freedesktop/secrets/collection/login")
	if err := conn.Export(fakeSecretService{collection}, secretServicePath, secretServiceInterface); err != nil {
		t.Fatal(err)
	}
	if err := conn.Export(fakeCollection{locked}, collection, "org.freedesktop.DBus.Properties"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.RequestName(secretServiceName, dbus.NameFlagDoNotQueue); err != nil {
		t.Fatal(err)
	}
}

func TestAwaitNamesAnotherLockedProvider(t *testing.T) {
	b := startActivatableBus(t)
	ownCollection(t, b, true)
	err := AwaitLoginKeyring(testDeadline(t, 2*keyringPoll), b.env, []byte("pw"), nil)
	comm, commErr := os.ReadFile("/proc/self/comm")
	if commErr != nil {
		t.Fatal(commErr)
	}
	if !errors.Is(err, errNoKeyring) || !strings.Contains(err.Error(), strings.TrimSpace(string(comm))) || !strings.Contains(err.Error(), "locked") {
		t.Errorf("a locked provider = %v, want it named", err)
	}
}

func TestAwaitAcceptsAnotherUnlockedProvider(t *testing.T) {
	b := startActivatableBus(t)
	ownCollection(t, b, false)
	if err := AwaitLoginKeyring(testDeadline(t, 2*keyringPoll), b.env, []byte("pw"), nil); err != nil {
		t.Errorf("an unlocked provider = %v, want no warning", err)
	}
}

func TestAwaitNamesAProviderWithoutADefaultCollection(t *testing.T) {
	t.Run("not speaking the Secret Service", func(t *testing.T) {
		b := startActivatableBus(t)
		if _, err := b.conn(t).RequestName(secretServiceName, dbus.NameFlagDoNotQueue); err != nil {
			t.Fatal(err)
		}
		err := AwaitLoginKeyring(testDeadline(t, 2*keyringPoll), b.env, []byte("pw"), nil)
		if !errors.Is(err, errNoKeyring) || !strings.Contains(err.Error(), "did not say whether it is locked") {
			t.Errorf("no Secret Service = %v, want that said", err)
		}
	})
	t.Run("no default alias", func(t *testing.T) {
		b := startActivatableBus(t)
		conn := b.conn(t)
		if err := conn.Export(fakeSecretService{noObject}, secretServicePath, secretServiceInterface); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.RequestName(secretServiceName, dbus.NameFlagDoNotQueue); err != nil {
			t.Fatal(err)
		}
		err := AwaitLoginKeyring(testDeadline(t, 2*keyringPoll), b.env, []byte("pw"), nil)
		if !errors.Is(err, errNoKeyring) || !strings.Contains(err.Error(), "no default alias") {
			t.Errorf("no default alias = %v, want that said", err)
		}
	})
}

type fakeKWallet struct{ open bool }

func (k fakeKWallet) NetworkWallet() (string, *dbus.Error) { return "kdewallet", nil }

func (k fakeKWallet) IsOpen(wallet string) (bool, *dbus.Error) {
	return k.open && wallet == "kdewallet", nil
}

func ownKWallet(t *testing.T, b *activatableBus, open bool) {
	t.Helper()
	conn := b.conn(t)
	methods := map[string]any{
		"networkWallet": fakeKWallet{open}.NetworkWallet,
		"isOpen":        fakeKWallet{open}.IsOpen,
	}
	if err := conn.ExportMethodTable(methods, kwalletServices[0].path, kwalletInterface); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.RequestName(kwalletServices[0].name, dbus.NameFlagDoNotQueue); err != nil {
		t.Fatal(err)
	}
}

func TestAwaitAcceptsAWalletPamKwalletOpened(t *testing.T) {
	b := startActivatableBus(t)
	ownKWallet(t, b, true)
	if err := AwaitLoginKeyring(testDeadline(t, 2*keyringPoll), b.env, []byte("pw"), nil); err != nil {
		t.Errorf("an open wallet = %v, want no warning", err)
	}
}

func TestAwaitNamesAClosedWallet(t *testing.T) {
	b := startActivatableBus(t)
	ownKWallet(t, b, false)
	err := AwaitLoginKeyring(testDeadline(t, 2*keyringPoll), b.env, []byte("pw"), nil)
	if !errors.Is(err, errNoKeyring) || !strings.Contains(err.Error(), kwalletServices[0].name) || !strings.Contains(err.Error(), "pam_kwallet") {
		t.Errorf("a closed wallet = %v, want KWallet and pam_kwallet named", err)
	}
}

func TestAwaitNamesAnInstalledKWalletWithoutStartingIt(t *testing.T) {
	b := startActivatableBus(t, fakeService{kwalletServices[1].name, "kwallet", "ok"})
	err := AwaitLoginKeyring(testDeadline(t, 2*keyringPoll), b.env, []byte("pw"), nil)
	if !errors.Is(err, errNoKeyring) || !strings.Contains(err.Error(), kwalletServices[1].name+" is installed") {
		t.Errorf("a dormant KWallet = %v, want it named", err)
	}
	if b.started("kwallet") {
		t.Error("KWallet was started though it cannot take the password")
	}
}

func TestLockScreenWaitOnlyForWhatActivationStarted(t *testing.T) {
	t.Run("nothing activatable ends at once", func(t *testing.T) {
		b := startActivatableBus(t)
		began := time.Now()
		err := awaitKeyring(testDeadline(t, unlockKeyringWait), b.env, []byte("pw"), nil, waitForActivation)
		if !errors.Is(err, errNoKeyring) || errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("no keyring = %v, want it said before the deadline", err)
		}
		if elapsed := time.Since(began); elapsed >= unlockKeyringWait {
			t.Errorf("the unlock waited %v for nothing", elapsed)
		}
	})
	t.Run("no bus ends at once", func(t *testing.T) {
		env := sessionEnv(map[string]string{"XDG_RUNTIME_DIR": sockdir.Dir(t)})
		began := time.Now()
		err := awaitKeyring(testDeadline(t, unlockKeyringWait), env, []byte("pw"), nil, waitForActivation)
		if !errors.Is(err, errNoKeyring) || !strings.Contains(err.Error(), "no session bus") {
			t.Errorf("no bus = %v", err)
		}
		if elapsed := time.Since(began); elapsed >= unlockKeyringWait {
			t.Errorf("the unlock waited %v for nothing", elapsed)
		}
	})
	t.Run("an activated keyring gets the password", func(t *testing.T) {
		b := startActivatableBus(t, fakeService{secretServiceName, "gnome", "ok"})
		if err := awaitKeyring(testDeadline(t, 15*time.Second), b.env, []byte("pw"), nil, waitForActivation); err != nil {
			t.Fatalf("awaitKeyring = %v", err)
		}
		if got := b.got(t, "gnome"); got != "pw" {
			t.Errorf("gnome-keyring got %q", got)
		}
	})
}

func TestKeyringThatDoesNotAnswerYetIsRetried(t *testing.T) {
	path := filepath.Join(sockdir.Dir(t), "control")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	if err := keyringRequest(path, keyringOpUnlock, []byte("pw")); !errors.Is(err, errKeyringAbsent) {
		t.Errorf("a socket unit's daemon still starting = %v, want errKeyringAbsent", err)
	}
}

func TestSessionBusAddress(t *testing.T) {
	cases := []struct {
		vars map[string]string
		want string
	}{
		{map[string]string{"DBUS_SESSION_BUS_ADDRESS": "unix:path=/x", "XDG_RUNTIME_DIR": "/run/user/1"}, "unix:path=/x"},
		{map[string]string{"XDG_RUNTIME_DIR": "/run/user/1"}, "unix:path=/run/user/1/bus"},
	}
	for _, c := range cases {
		if got, err := sessionBusAddress(sessionEnv(c.vars)); err != nil || got != c.want {
			t.Errorf("%v: %q, %v; want %q", c.vars, got, err, c.want)
		}
	}
	if got, err := sessionBusAddress(sessionEnv(map[string]string{"XDG_RUNTIME_DIR": "relative"})); err == nil {
		t.Errorf("no absolute runtime dir = %q, want an error", got)
	}
}

// racingSecretService answers its first ReadAlias as gnome-keyring does
// when the read beats the client's entry: "/", whatever the alias holds.
type racingSecretService struct {
	collection dbus.ObjectPath
	reads      atomic.Int32
}

func (s *racingSecretService) ReadAlias(string) (dbus.ObjectPath, *dbus.Error) {
	if s.reads.Add(1) == 1 {
		return noObject, nil
	}
	return s.collection, nil
}

func ownRacingCollection(t *testing.T, b *activatableBus, locked bool) {
	t.Helper()
	conn := b.conn(t)
	collection := dbus.ObjectPath("/org/freedesktop/secrets/collection/login")
	if err := conn.Export(&racingSecretService{collection: collection}, secretServicePath, secretServiceInterface); err != nil {
		t.Fatal(err)
	}
	if err := conn.Export(fakeCollection{locked}, collection, "org.freedesktop.DBus.Properties"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.RequestName(secretServiceName, dbus.NameFlagDoNotQueue); err != nil {
		t.Fatal(err)
	}
}

func TestAwaitReadsTheCollectionBehindAFirstEmptyAlias(t *testing.T) {
	t.Run("unlocked", func(t *testing.T) {
		b := startActivatableBus(t)
		ownRacingCollection(t, b, false)
		if err := AwaitLoginKeyring(testDeadline(t, 2*keyringPoll), b.env, []byte("pw"), nil); err != nil {
			t.Errorf("an unlocked keyring = %v, want no warning", err)
		}
	})
	t.Run("locked", func(t *testing.T) {
		b := startActivatableBus(t)
		ownRacingCollection(t, b, true)
		err := AwaitLoginKeyring(testDeadline(t, 2*keyringPoll), b.env, []byte("pw"), nil)
		if !errors.Is(err, errNoKeyring) || !strings.Contains(err.Error(), "default collection is locked") {
			t.Errorf("a locked keyring = %v, want it reported locked", err)
		}
	})
}
