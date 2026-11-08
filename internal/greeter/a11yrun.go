package greeter

import (
	"errors"
	"log"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/stubbedev/lys-desktop/service/lyswm"
	"github.com/stubbedev/lys-ui/app"
	"github.com/stubbedev/lys-ui/widget"
)

const readerStopGrace = 3 * time.Second

type textScale struct {
	base, scale float64
}

func followTextScale(a *app.Application) func(scale float64) {
	ts := &textScale{base: a.DesktopFontSize(), scale: 1}
	widget.SetRootFontSize(ts.base)
	a.OnDesktopFontSizeChange(func(px float64) {
		ts.base = px
		widget.SetRootFontSize(ts.base * ts.scale)
	})
	return func(scale float64) {
		ts.scale = scale
		widget.SetRootFontSize(ts.base * ts.scale)
	}
}

type readers struct {
	wg sync.WaitGroup
}

func (r *readers) wait() { r.wg.Wait() }

func (r *readers) start(a *app.Application, path string) func() (stop func()) {
	return func() func() {
		a.ServeAccessibility(app.A11YOptions{}, func(err error) {
			if err != nil {
				log.Printf("greeter: accessibility: serve the AT-SPI bridge: %v", err)
			}
		})
		cmd := exec.Command(path)
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		if err := cmd.Start(); err != nil {
			log.Printf("greeter: accessibility: start the screen reader %s: %v", path, err)
			return func() {}
		}
		log.Printf("greeter: accessibility: screen reader %s started (pid %d)", path, cmd.Process.Pid)
		done := make(chan struct{})
		r.wg.Go(func() {
			err := cmd.Wait()
			close(done)
			var exit *exec.ExitError
			if err != nil && !errors.As(err, &exit) {
				log.Printf("greeter: accessibility: the screen reader %s: %v", path, err)
			}
		})
		return func() {
			if err := cmd.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
				log.Printf("greeter: accessibility: stop the screen reader: %v", err)
			}
			r.wg.Go(func() {
				select {
				case <-done:
				case <-time.After(readerStopGrace):
					if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
						log.Printf("greeter: accessibility: kill the screen reader: %v", err)
					}
				}
			})
		}
	}
}

func wmDispatch() func(action string) {
	if lyswm.SocketPath() == "" {
		return nil
	}
	return func(action string) {
		go func() {
			if err := lyswm.Dispatch(action); err != nil {
				log.Printf("greeter: accessibility: lys-wm %s: %v", action, err)
			}
		}()
	}
}

func openPopover(from *widget.Button, content, focus widget.Widget, onClosed func()) func() {
	return widget.OpenPopover(from, from, content, widget.PopoverOptions{Focus: focus, Gap: widget.PopoverGap, Side: widget.PopoverAbove, OnClosed: onClosed})
}

func screenReaderPath(name string) (string, bool) {
	if name == "" {
		return "", false
	}
	path, err := exec.LookPath(name)
	if err != nil {
		log.Printf("greeter: accessibility: no screen reader: %v", err)
		return "", false
	}
	return path, true
}
