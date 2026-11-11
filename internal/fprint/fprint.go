package fprint

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"time"

	"github.com/stubbedev/lys-lib/dbusx"
	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
)

const (
	BusName       = "net.reactivated.Fprint"
	ManagerPath   = dbus.ObjectPath("/net/reactivated/Fprint/Manager")
	ManagerIface  = BusName + ".Manager"
	DeviceIface   = BusName + ".Device"
	errNoDevice   = BusName + ".Error.NoSuchDevice"
	errNoPrints   = BusName + ".Error.NoEnrolledPrints"
	errNoService  = "org.freedesktop.DBus.Error.ServiceUnknown"
	errNoName     = "org.freedesktop.DBus.Error.NameHasNoOwner"
	anyFinger     = "any"
	callTimeout   = 10 * time.Second
	settleTimeout = 5 * time.Second
)

type Result uint8

const (
	Match Result = iota + 1
	NoMatch
	Retry
)

type verifyStatus struct {
	Result string `dbus:"result"`
	Done   bool   `dbus:"done"`
}

var verifyStatusSignal = dbusx.NewSignal[verifyStatus](DeviceIface, "VerifyStatus")

var ErrUnavailable = errors.New("fprint: no fingerprint reader or no enrolled finger")

func Verify(ctx context.Context, conn *dbus.Conn, user string, onResult func(Result)) error {
	dev, err := defaultDevice(ctx, conn)
	if err != nil {
		return err
	}
	d := conn.Object(BusName, dev)
	if err := enrolled(ctx, d, user); err != nil {
		return err
	}
	if err := call(ctx, d, "Claim", user); err != nil {
		return err
	}
	defer settle(ctx, d, "Release")
	statuses := make(chan verifyStatus)
	watching, stopWatching := context.WithCancel(context.WithoutCancel(ctx))
	w, err := dbusx.WatchSignals(watching, conn, dbusx.FromOwner(BusName),
		func(err error) { log.Printf("fprint: a status of %s: %v", dev, err) },
		dbusx.On(verifyStatusSignal, dev, func(s verifyStatus, _ dbus.Sequence) error {
			select {
			case statuses <- s:
			case <-watching.Done():
			}
			return nil
		}))
	if err != nil {
		stopWatching()
		return fmt.Errorf("fprint: follow %s on %s: %w", verifyStatusSignal.Name(), dev, err)
	}
	defer func() {
		stopWatching()
		if err := w.Stop(); err != nil && !errors.Is(err, dbusx.ErrDisconnected) {
			log.Printf("fprint: stop following %s on %s: %v", verifyStatusSignal.Name(), dev, err)
		}
	}()
	if stopped, err := scan(ctx, d); stopped || err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			settle(ctx, d, "VerifyStop")
			return nil
		case <-w.Done():
			return fmt.Errorf("fprint: follow %s on %s: %w", verifyStatusSignal.Name(), dev, w.Stop())
		case st := <-statuses:
			again, err := deliver(st.Result, onResult)
			if err != nil || !again {
				settle(ctx, d, "VerifyStop")
				return err
			}
			if !st.Done {
				continue
			}
			settle(ctx, d, "VerifyStop")
			if ctx.Err() != nil {
				return nil
			}
			if stopped, err := scan(ctx, d); stopped || err != nil {
				return err
			}
		}
	}
}

func scan(ctx context.Context, d dbus.BusObject) (stopped bool, err error) {
	err = call(ctx, d, "VerifyStart", anyFinger)
	if err != nil && ctx.Err() != nil {
		settle(ctx, d, "VerifyStop")
		return true, nil
	}
	return false, err
}

func deliver(result string, onResult func(Result)) (again bool, err error) {
	switch result {
	case "verify-match":
		onResult(Match)
		return false, nil
	case "verify-no-match":
		onResult(NoMatch)
	case "verify-retry-scan", "verify-swipe-too-short", "verify-finger-not-centered", "verify-remove-and-retry":
		onResult(Retry)
	default:
		return false, fmt.Errorf("fprint: verification ended with %s", result)
	}
	return true, nil
}

func defaultDevice(ctx context.Context, conn *dbus.Conn) (dbus.ObjectPath, error) {
	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	var dev dbus.ObjectPath
	err := conn.Object(BusName, ManagerPath).CallWithContext(callCtx, ManagerIface+".GetDefaultDevice", dbus.FlagNoAutoStart).Store(&dev)
	if isNamed(err, errNoDevice, errNoService, errNoName) {
		return "", fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if err != nil {
		return "", fmt.Errorf("fprint: GetDefaultDevice: %w", err)
	}
	return dev, nil
}

func enrolled(ctx context.Context, d dbus.BusObject, user string) error {
	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	var fingers []string
	err := d.CallWithContext(callCtx, DeviceIface+".ListEnrolledFingers", 0, user).Store(&fingers)
	if isNamed(err, errNoPrints) || (err == nil && len(fingers) == 0) {
		return fmt.Errorf("%w: %s has no finger enrolled on %s", ErrUnavailable, user, d.Path())
	}
	if err != nil {
		return fmt.Errorf("fprint: ListEnrolledFingers(%s) on %s: %w", user, d.Path(), err)
	}
	return nil
}

func call(ctx context.Context, d dbus.BusObject, method string, args ...any) error {
	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	if err := d.CallWithContext(callCtx, DeviceIface+"."+method, 0, args...).Err; err != nil {
		return fmt.Errorf("fprint: %s on %s: %w", method, d.Path(), err)
	}
	return nil
}

func settle(ctx context.Context, d dbus.BusObject, method string) {
	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), settleTimeout)
	defer cancel()
	if err := d.CallWithContext(settleCtx, DeviceIface+"."+method, 0).Err; err != nil {
		log.Printf("fprint: %s on %s: %v", method, d.Path(), err)
	}
}

func isNamed(err error, names ...string) bool {
	de, ok := errors.AsType[dbus.Error](err)
	if !ok {
		return false
	}
	return slices.Contains(names, de.Name)
}
