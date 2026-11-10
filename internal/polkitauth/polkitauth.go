// Package polkitauth asks polkitd whether a process may perform an
// action, letting polkit's agent in that process's session prompt.
package polkitauth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
)

const (
	authorityName  = "org.freedesktop.PolicyKit1"
	authorityPath  = dbus.ObjectPath("/org/freedesktop/PolicyKit1/Authority")
	authorityIface = "org.freedesktop.PolicyKit1.Authority"

	allowUserInteraction uint32 = 1

	dismissedDetail = "polkit.dismissed"

	cancelTimeout = 5 * time.Second
)

type Action string

type Process struct {
	PID       uint32
	StartTime uint64
	UID       uint32
}

func ProcessOf(pid int32, uid uint32) (Process, error) {
	path := "/proc/" + strconv.Itoa(int(pid)) + "/stat"
	stat, err := os.ReadFile(path) //nolint:gosec // procfs of a pid the kernel named
	if err != nil {
		return Process{}, fmt.Errorf("read the start time of pid %d: %w", pid, err)
	}
	start, err := startTime(stat)
	if err != nil {
		return Process{}, fmt.Errorf("%s: %w", path, err)
	}
	return Process{PID: uint32(pid), StartTime: start, UID: uid}, nil //nolint:gosec // a kernel pid is positive
}

const startTimeField = 22

func startTime(stat []byte) (uint64, error) {
	_, afterComm, ok := bytes.CutLast(stat, []byte(")"))
	if !ok {
		return 0, errors.New("no command name in the process's stat")
	}
	fields := bytes.Fields(afterComm)
	const firstAfterComm = 3
	i := startTimeField - firstAfterComm
	if len(fields) <= i {
		return 0, fmt.Errorf("the process's stat has %d fields after its command name, want over %d", len(fields), i)
	}
	start, err := strconv.ParseUint(string(fields[i]), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("start time: %w", err)
	}
	return start, nil
}

type subject struct {
	Kind    string
	Details map[string]dbus.Variant
}

func (p Process) subject() subject {
	return subject{Kind: "unix-process", Details: map[string]dbus.Variant{
		"pid":        dbus.MakeVariant(p.PID),
		"start-time": dbus.MakeVariant(p.StartTime),
		"uid":        dbus.MakeVariant(int32(p.UID)), //nolint:gosec // polkit's uid is an int32
	}}
}

type result struct {
	IsAuthorized bool
	IsChallenge  bool
	Details      map[string]string
}

type Verdict uint8

const (
	Authorized Verdict = iota
	Denied
	Dismissed
	NoAgent
)

var cancellations atomic.Uint64

func Check(ctx context.Context, conn *dbus.Conn, action Action, p Process) (Verdict, error) {
	id := "lys-login-" + strconv.FormatUint(cancellations.Add(1), 10)
	authority := conn.Object(authorityName, authorityPath)
	var r result
	call := authority.CallWithContext(ctx, authorityIface+".CheckAuthorization", 0,
		p.subject(), string(action), map[string]string{}, allowUserInteraction, id)
	if call.Err != nil {
		err := fmt.Errorf("polkit: check %s for pid %d (uid %d): %w", action, p.PID, p.UID, call.Err)
		if ctx.Err() != nil {
			return 0, errors.Join(err, cancel(conn, id))
		}
		return 0, err
	}
	if err := call.Store(&r); err != nil {
		return 0, fmt.Errorf("polkit: read the verdict on %s for pid %d: %w", action, p.PID, err)
	}
	switch {
	case r.IsAuthorized:
		return Authorized, nil
	case r.Details[dismissedDetail] == "true":
		return Dismissed, nil
	case r.IsChallenge:
		return NoAgent, nil
	default:
		return Denied, nil
	}
}

func cancel(conn *dbus.Conn, id string) error {
	ctx, stop := context.WithTimeout(context.Background(), cancelTimeout)
	defer stop()
	if err := conn.Object(authorityName, authorityPath).CallWithContext(ctx, authorityIface+".CancelCheckAuthorization", 0, id).Err; err != nil {
		return fmt.Errorf("polkit: cancel check %s: %w", id, err)
	}
	return nil
}
