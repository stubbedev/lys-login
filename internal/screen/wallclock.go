package screen

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// WallClock is the time a clock on screen shows, which unlike the timers of the
// runtime is what the user reads off their wall: it moves on through a suspend, and
// jumps when NTP or an administrator sets it or the time zone changes.
type WallClock interface {
	// Now is the time in the current zone.
	Now() time.Time
	// Arm runs fn on the loop when the wall clock reaches at, or at once when it
	// or the zone jumps, replacing what was armed.
	Arm(at time.Time, fn func())
	// Disarm forgets what was armed; a firing on its way finds nothing.
	Disarm()
}

const localtimePath = "/etc/localtime"

// SystemClock is the WallClock of this machine. An absolute timer on
// CLOCK_REALTIME fires on the wall clock's time however long the machine slept, and
// TFD_TIMER_CANCEL_ON_SET fails its read the moment the clock is set; a watch on
// /etc/localtime reloads the zone, which the runtime only reads once.
type SystemClock struct {
	invoke func(func())
	timer  *os.File
	fd     int
	zone   atomic.Pointer[time.Location]
	path   string
	watch  *os.File
	wg     sync.WaitGroup
	fn     func()
}

// NewSystemClock starts a clock that runs what is armed through invoke, on the loop.
func NewSystemClock(invoke func(func())) (*SystemClock, error) {
	return newSystemClock(invoke, localtimePath)
}

func newSystemClock(invoke func(func()), zonePath string) (*SystemClock, error) {
	fd, err := unix.TimerfdCreate(unix.CLOCK_REALTIME, unix.TFD_CLOEXEC|unix.TFD_NONBLOCK)
	if err != nil {
		return nil, fmt.Errorf("screen: create a wall clock timer: %w", err)
	}
	c := &SystemClock{invoke: invoke, fd: fd, timer: os.NewFile(uintptr(fd), "timerfd"), path: zonePath}
	c.zone.Store(time.Local)
	c.wg.Go(c.readTimer)
	if _, fixed := os.LookupEnv("TZ"); !fixed {
		if err := c.watchZone(); err != nil {
			log.Printf("screen: the clock will not follow a change of time zone: %v", err)
		}
	}
	return c, nil
}

// Now is the time in the current zone.
func (c *SystemClock) Now() time.Time {
	return time.Now().In(c.zone.Load())
}

// Arm runs fn on the loop at at, or when the clock or the zone jumps.
func (c *SystemClock) Arm(at time.Time, fn func()) {
	c.fn = fn
	spec := unix.ItimerSpec{Value: unix.NsecToTimespec(at.UnixNano())}
	if err := unix.TimerfdSettime(c.fd, unix.TFD_TIMER_ABSTIME|unix.TFD_TIMER_CANCEL_ON_SET, &spec, nil); err != nil {
		log.Printf("screen: arm the wall clock timer for %v: %v", at, err)
	}
}

// Disarm forgets what was armed.
func (c *SystemClock) Disarm() {
	c.fn = nil
	if err := unix.TimerfdSettime(c.fd, 0, &unix.ItimerSpec{}, nil); err != nil {
		log.Printf("screen: disarm the wall clock timer: %v", err)
	}
}

func (c *SystemClock) fire() {
	fn := c.fn
	c.fn = nil
	if fn != nil {
		fn()
	}
}

func (c *SystemClock) readTimer() {
	buf := make([]byte, 8)
	for {
		_, err := c.timer.Read(buf)
		switch {
		case err == nil, errors.Is(err, unix.ECANCELED):
			c.invoke(c.fire)
		case errors.Is(err, os.ErrClosed):
			return
		default:
			log.Printf("screen: read the wall clock timer: %v", err)
			return
		}
	}
}

func (c *SystemClock) watchZone() error {
	dir := filepath.Dir(c.path)
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		return fmt.Errorf("inotify for %s: %w", c.path, err)
	}
	mask := uint32(unix.IN_CREATE | unix.IN_MOVED_TO | unix.IN_CLOSE_WRITE)
	if _, err := unix.InotifyAddWatch(fd, dir, mask); err != nil {
		return errors.Join(fmt.Errorf("watch %s: %w", dir, err), unix.Close(fd))
	}
	c.watch = os.NewFile(uintptr(fd), "inotify")
	c.wg.Go(c.readZone)
	return nil
}

func (c *SystemClock) readZone() {
	buf := make([]byte, 4096)
	name := filepath.Base(c.path)
	for {
		n, err := c.watch.Read(buf)
		if errors.Is(err, os.ErrClosed) {
			return
		}
		if err != nil {
			log.Printf("screen: watch the time zone: %v", err)
			return
		}
		if !namesFile(buf[:n], name) {
			continue
		}
		if err := c.reloadZone(); err != nil {
			log.Printf("screen: the clock stays in its time zone: %v", err)
			continue
		}
		c.invoke(c.fire)
	}
}

func (c *SystemClock) reloadZone() error {
	data, err := os.ReadFile(c.path)
	if err != nil {
		return fmt.Errorf("read the time zone: %w", err)
	}
	zone, err := time.LoadLocationFromTZData("Local", data)
	if err != nil {
		return fmt.Errorf("parse the time zone in %s: %w", c.path, err)
	}
	c.zone.Store(zone)
	return nil
}

// namesFile says whether inotify events in buf name file.
func namesFile(buf []byte, file string) bool {
	for len(buf) >= unix.SizeofInotifyEvent {
		event := (*unix.InotifyEvent)(unsafe.Pointer(&buf[0])) //nolint:gosec // the kernel writes whole, aligned events
		end := unix.SizeofInotifyEvent + int(event.Len)
		if end > len(buf) {
			return false
		}
		if strings.TrimRight(string(buf[unix.SizeofInotifyEvent:end]), "\x00") == file {
			return true
		}
		buf = buf[end:]
	}
	return false
}

// Close stops the clock; nothing armed runs after it.
func (c *SystemClock) Close() error {
	errs := []error{c.timer.Close()}
	if c.watch != nil {
		errs = append(errs, c.watch.Close())
	}
	c.wg.Wait()
	return errors.Join(errs...)
}
