package lock

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/stubbedev/lys-desktop/config"
	"github.com/stubbedev/lys-desktop/service/mpris"
	"github.com/stubbedev/lys-desktop/service/upower"
	"github.com/stubbedev/lys-lib/dbusx"
	dbus "github.com/stubbedev/lys-lib/third_party/godbus"

	"github.com/stubbedev/lys-login/internal/fprint"
)

const (
	controlTimeout = 5 * time.Second
	dialTimeout    = 5 * time.Second
)

type busDial func(context.Context) (*dbus.Conn, error)

func dialSessionBus(context.Context) (*dbus.Conn, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, fmt.Errorf("connect to the session bus: %w", err)
	}
	return conn, nil
}

func dialSystemBus(ctx context.Context) (*dbus.Conn, error) {
	return dbusx.DialSystem(ctx, dialTimeout)
}

type busWatches struct {
	invoke func(func())
	wg     sync.WaitGroup
}

func (b *busWatches) wait() { b.wg.Wait() }

func (b *busWatches) run(what string, dial busDial, body func(ctx context.Context, conn *dbus.Conn) error) (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	b.wg.Go(func() {
		conn, err := dial(ctx)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("lock: %s: %v", what, err)
			}
			return
		}
		defer func() {
			if err := conn.Close(); err != nil {
				log.Printf("lock: %s: close the bus connection: %v", what, err)
			}
		}()
		if err := body(ctx, conn); err != nil && ctx.Err() == nil {
			log.Printf("lock: %s: %v", what, err)
		}
	})
	return cancel
}

func (b *busWatches) post(ctx context.Context, fn func()) {
	b.invoke(func() {
		if ctx.Err() == nil {
			fn()
		}
	})
}

type mediaControl struct {
	what string
	bus  string
	run  func(*mpris.Service, context.Context, string) error
}

type sessionMedia struct {
	watches  *busWatches
	cfg      config.MediaConfig
	controls chan mediaControl
}

func newSessionMedia(watches *busWatches, cfg config.MediaConfig) *sessionMedia {
	return &sessionMedia{watches: watches, cfg: cfg, controls: make(chan mediaControl, 4)}
}

func (m *sessionMedia) Watch(onChange func(nowPlaying, bool)) (stop func()) {
	return m.watches.run("now playing", dialSessionBus, func(ctx context.Context, conn *dbus.Conn) error {
		svc, err := mpris.New(conn, m.cfg.PlayersIgnored, m.cfg.PlayerPriority)
		if err != nil {
			return err
		}
		defer func() {
			if err := svc.Close(); err != nil {
				log.Printf("lock: now playing: %v", err)
			}
		}()
		ticks, unsubscribe := svc.Subscribe()
		defer unsubscribe()
		shown := ""
		push := func() {
			np, ok := nowPlayingOf(svc.Active())
			if np.bus != shown {
				shown = np.bus
				log.Printf("lock: now playing from %q", shown)
			}
			m.watches.post(ctx, func() { onChange(np, ok) })
		}
		push()
		for {
			select {
			case <-ctx.Done():
				return nil
			case _, open := <-ticks:
				if !open {
					return nil
				}
				push()
			case c := <-m.controls:
				callCtx, cancel := context.WithTimeout(ctx, controlTimeout)
				if err := c.run(svc, callCtx, c.bus); err != nil {
					log.Printf("lock: %s on %s: %v", c.what, c.bus, err)
				}
				cancel()
			}
		}
	})
}

func (m *sessionMedia) control(c mediaControl) {
	select {
	case m.controls <- c:
	default:
		log.Printf("lock: %s on %s dropped: %d controls are still waiting", c.what, c.bus, cap(m.controls))
	}
}

func (m *sessionMedia) PlayPause(bus string) {
	m.control(mediaControl{what: "play/pause", bus: bus, run: (*mpris.Service).PlayPause})
}

func (m *sessionMedia) Next(bus string) {
	m.control(mediaControl{what: "next track", bus: bus, run: (*mpris.Service).Next})
}

func (b *busWatches) battery(onChange func(batteryLevel, bool)) (stop func()) {
	return b.run("battery", dialSystemBus, func(ctx context.Context, conn *dbus.Conn) error {
		src := upower.NewOn(conn)
		ticks, unsubscribe, err := src.Subscribe(ctx)
		if err != nil {
			return err
		}
		defer unsubscribe()
		for {
			dev, err := src.Read(ctx)
			if de, ok := errors.AsType[dbus.Error](err); ok && de.Name == "org.freedesktop.DBus.Error.ServiceUnknown" {
				log.Printf("lock: battery: no UPower on the system bus")
				return nil
			}
			if err != nil {
				return err
			}
			level, present := batteryLevelOf(dev)
			b.post(ctx, func() { onChange(level, present) })
			select {
			case <-ctx.Done():
				return nil
			case _, open := <-ticks:
				if !open {
					return nil
				}
			}
		}
	})
}

func (b *busWatches) fingerprint(user string) func(onResult func(fprint.Result)) (stop func()) {
	return func(onResult func(fprint.Result)) func() {
		return b.run("fingerprint", dialSystemBus, func(ctx context.Context, conn *dbus.Conn) error {
			log.Printf("lock: fingerprint: asking fprintd to verify %s", user)
			err := fprint.Verify(ctx, conn, user, func(r fprint.Result) { b.post(ctx, func() { onResult(r) }) })
			if errors.Is(err, fprint.ErrUnavailable) {
				log.Printf("lock: fingerprint: %v; the password alone unlocks", err)
				return nil
			}
			return err
		})
	}
}
