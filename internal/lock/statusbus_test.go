package lock

import (
	"maps"
	"net"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stubbedev/lys-desktop/config"
	"github.com/stubbedev/lys-desktop/service/mpris"
	"github.com/stubbedev/lys-desktop/service/upower"
	"github.com/stubbedev/lys-lib/dbustest"
	"github.com/stubbedev/lys-lib/sockdir"
	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
	"github.com/stubbedev/lys-lib/third_party/godbus/prop"

	"github.com/stubbedev/lys-login/internal/fprint"
)

const waitBound = 10 * time.Second

type loopQueue chan func()

func (q loopQueue) Invoke(fn func()) { q <- fn }

func (q loopQueue) next(t *testing.T, what string) {
	t.Helper()
	select {
	case fn := <-q:
		fn()
	case <-time.After(waitBound):
		t.Fatalf("no %s within %v", what, waitBound)
	}
}

type fakePlayer struct {
	mu    sync.Mutex
	calls []string
	props *prop.Properties
}

func (f *fakePlayer) record(call string) *dbus.Error {
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()
	return nil
}

func (f *fakePlayer) PlayPause() *dbus.Error { return f.record("PlayPause") }
func (f *fakePlayer) Next() *dbus.Error      { return f.record("Next") }

func (f *fakePlayer) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func startPlayer(t *testing.T, bus *dbustest.Bus, name, title, status string) *fakePlayer {
	t.Helper()
	conn := bus.Conn(t)
	f := &fakePlayer{}
	if err := conn.Export(f, mpris.ObjectPath, mpris.PlayerIface); err != nil {
		t.Fatal(err)
	}
	props, err := prop.Export(conn, mpris.ObjectPath, prop.Map{
		mpris.RootIface: {"Identity": {Value: "Fake", Emit: prop.EmitConst}},
		mpris.PlayerIface: {
			"PlaybackStatus": {Value: status, Emit: prop.EmitTrue},
			"Position":       {Value: int64(0), Emit: prop.EmitFalse},
			"CanGoNext":      {Value: true, Emit: prop.EmitTrue},
			"Metadata": {Value: map[string]dbus.Variant{
				"xesam:title":  dbus.MakeVariant(title),
				"xesam:artist": dbus.MakeVariant([]string{"Miles Davis"}),
				"mpris:artUrl": dbus.MakeVariant("file:///covers/kind%20of%20blue.png"),
			}, Emit: prop.EmitTrue},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.props = props
	if reply, err := conn.RequestName(name, dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("own %s: %v %v", name, reply, err)
	}
	return f
}

func awaitCalls(t *testing.T, p *fakePlayer, want []string) {
	t.Helper()
	end := time.Now().Add(waitBound)
	for !slices.Equal(p.recorded(), want) {
		if time.Now().After(end) {
			t.Fatalf("player calls %v, want %v", p.recorded(), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type mediaSeen struct {
	np nowPlaying
	ok bool
}

func awaitMedia(t *testing.T, loop loopQueue, seen chan mediaSeen, what string, done func(mediaSeen) bool) {
	t.Helper()
	end := time.Now().Add(waitBound)
	var last mediaSeen
	for time.Now().Before(end) {
		loop.next(t, what)
		if last = <-seen; done(last) {
			return
		}
	}
	t.Fatalf("no %s; last saw %+v", what, last)
}

func TestTheSessionsPlayerReachesTheLockAndItsButtons(t *testing.T) {
	bus := dbustest.Session(t)
	name := mpris.BusNamePrefix + "fake"
	player := startPlayer(t, bus, name, "Blue in Green", "Playing")
	loop := make(loopQueue, 8)
	watches := &busWatches{invoke: loop.Invoke}
	media := newSessionMedia(watches, config.Defaults().Media)
	seen := make(chan mediaSeen, 8)
	stop := media.Watch(func(np nowPlaying, ok bool) { seen <- mediaSeen{np, ok} })
	want := nowPlaying{bus: name, title: "Blue in Green", artist: "Miles Davis", artPath: "/covers/kind of blue.png", playing: true, canNext: true}
	awaitMedia(t, loop, seen, "the playing track", func(s mediaSeen) bool { return s.ok && s.np == want })
	media.PlayPause(name)
	media.Next(name)
	awaitCalls(t, player, []string{"PlayPause", "Next"})
	player.props.SetMust(mpris.PlayerIface, "PlaybackStatus", "Paused")
	awaitMedia(t, loop, seen, "the paused track", func(s mediaSeen) bool { return s.ok && !s.np.playing })
	stop()
	watches.wait()
}

func TestNoPlayerShowsNoMediaRow(t *testing.T) {
	dbustest.Session(t)
	loop := make(loopQueue, 8)
	watches := &busWatches{invoke: loop.Invoke}
	media := newSessionMedia(watches, config.Defaults().Media)
	seen := make(chan mediaSeen, 8)
	stop := media.Watch(func(np nowPlaying, ok bool) { seen <- mediaSeen{np, ok} })
	loop.next(t, "first snapshot")
	if got := <-seen; got.ok {
		t.Errorf("no player on the bus shows %+v", got.np)
	}
	stop()
	watches.wait()
}

func TestAStoppedWatchPostsNothing(t *testing.T) {
	bus := dbustest.Session(t)
	startPlayer(t, bus, mpris.BusNamePrefix+"fake", "Blue in Green", "Playing")
	loop := make(loopQueue, 8)
	watches := &busWatches{invoke: loop.Invoke}
	media := newSessionMedia(watches, config.Defaults().Media)
	stop := media.Watch(func(nowPlaying, bool) { t.Error("a stopped watch reached the lock") })
	stop()
	watches.wait()
	for len(loop) > 0 {
		(<-loop)()
	}
}

type fakeUPower struct {
	mu    sync.Mutex
	props map[string]dbus.Variant
	conn  *dbus.Conn
}

func (u *fakeUPower) GetAll(string) (map[string]dbus.Variant, *dbus.Error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return maps.Clone(u.props), nil
}

func (u *fakeUPower) set(t *testing.T, percent float64, state uint32) {
	t.Helper()
	u.mu.Lock()
	u.props["Percentage"] = dbus.MakeVariant(percent)
	u.props["State"] = dbus.MakeVariant(state)
	u.mu.Unlock()
	changed := map[string]dbus.Variant{"Percentage": dbus.MakeVariant(percent)}
	if err := u.conn.Emit(upower.DisplayDevicePath, "org.freedesktop.DBus.Properties.PropertiesChanged", "org.freedesktop.UPower.Device", changed, []string{}); err != nil {
		t.Fatal(err)
	}
}

func startUPower(t *testing.T, bus *dbustest.Bus, present bool) *fakeUPower {
	t.Helper()
	conn := bus.Conn(t)
	u := &fakeUPower{conn: conn, props: map[string]dbus.Variant{
		"IsPresent":  dbus.MakeVariant(present),
		"Percentage": dbus.MakeVariant(57.4),
		"State":      dbus.MakeVariant(uint32(2)),
	}}
	if err := conn.ExportMethodTable(map[string]any{"GetAll": u.GetAll}, upower.DisplayDevicePath, "org.freedesktop.DBus.Properties"); err != nil {
		t.Fatal(err)
	}
	if reply, err := conn.RequestName(upower.BusName, dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("own %s: %v %v", upower.BusName, reply, err)
	}
	return u
}

type batterySeen struct {
	level batteryLevel
	ok    bool
}

func watchBattery(t *testing.T) (chan batterySeen, loopQueue, func()) {
	t.Helper()
	loop := make(loopQueue, 8)
	watches := &busWatches{invoke: loop.Invoke}
	seen := make(chan batterySeen, 8)
	stop := watches.battery(func(l batteryLevel, ok bool) { seen <- batterySeen{l, ok} })
	return seen, loop, func() { stop(); watches.wait() }
}

func TestTheBatteryFollowsUPower(t *testing.T) {
	bus := dbustest.Start(t)
	bus.UseAsSystemBus(t)
	u := startUPower(t, bus, true)
	seen, loop, stop := watchBattery(t)
	defer stop()
	loop.next(t, "the first reading")
	if got := <-seen; !got.ok || got.level != (batteryLevel{percent: 57}) {
		t.Fatalf("first reading %+v %v", got.level, got.ok)
	}
	u.set(t, 58, 1)
	loop.next(t, "the charging reading")
	if got := <-seen; !got.ok || got.level != (batteryLevel{percent: 58, charging: true}) {
		t.Fatalf("charging reading %+v %v", got.level, got.ok)
	}
}

func TestADesktopWithoutABatteryShowsNone(t *testing.T) {
	bus := dbustest.Start(t)
	bus.UseAsSystemBus(t)
	startUPower(t, bus, false)
	seen, loop, stop := watchBattery(t)
	defer stop()
	loop.next(t, "the first reading")
	if got := <-seen; got.ok {
		t.Errorf("no battery shows %+v", got.level)
	}
}

func TestWithoutUPowerTheBatteryIsNeverShown(t *testing.T) {
	bus := dbustest.Start(t)
	bus.UseAsSystemBus(t)
	seen, loop, stop := watchBattery(t)
	stop()
	if len(seen) != 0 || len(loop) != 0 {
		t.Error("a bus without UPower reported a battery")
	}
}

func TestWithoutFprintdNoResultReachesTheLock(t *testing.T) {
	bus := dbustest.Start(t)
	bus.UseAsSystemBus(t)
	loop := make(loopQueue, 8)
	watches := &busWatches{invoke: loop.Invoke}
	watches.fingerprint("ada")(func(r fprint.Result) { t.Errorf("result %v without fprintd", r) })
	watches.wait()
	if len(loop) != 0 {
		t.Error("a bus without fprintd posted to the lock")
	}
}

func TestStoppingAWatchEndsItsDialOfASilentBus(t *testing.T) {
	sock := filepath.Join(sockdir.Dir(t), "silent")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", "unix:path="+sock)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+sock)
	watches := &busWatches{invoke: func(func()) { t.Error("a silent bus posted to the lock") }}
	stopBattery := watches.battery(func(batteryLevel, bool) {})
	stopFingerprint := watches.fingerprint("ada")(func(fprint.Result) {})
	stopMedia := newSessionMedia(watches, config.MediaConfig{}).Watch(func(nowPlaying, bool) {})
	stopBattery()
	stopFingerprint()
	stopMedia()
	ended := make(chan struct{})
	go func() {
		watches.wait()
		close(ended)
	}()
	select {
	case <-ended:
	case <-time.After(waitBound):
		t.Fatalf("the watches still dial a silent bus %v after they were stopped", waitBound)
	}
}
