package layouts

import (
	"log"
	"os"

	"github.com/stubbedev/lys-lib/xkb"
	"github.com/stubbedev/lys-shell/service/lyswm"
)

func LabeledOnSystem(names []string) ([]Layout, error) {
	return Labeled(names, xkb.NewContext(os.Getenv).IncludePaths())
}

func Switcher(mode string) func(steps int) {
	if lyswm.SocketPath() == "" {
		return nil
	}
	return func(steps int) {
		go func() {
			for range steps {
				if err := lyswm.Dispatch("nextlayout"); err != nil {
					log.Printf("%s: switch the keyboard layout: %v", mode, err)
					return
				}
			}
		}()
	}
}
