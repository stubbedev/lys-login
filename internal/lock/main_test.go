package lock

import (
	"log"
	"os"
	"testing"

	"github.com/stubbedev/lys-shell/testhome"
)

// TestMain runs the package against a scratch home: building a theme
// or a config reads, and the shell scaffolds, the user's config dir.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "vault-unlock":
			if err := UnlockVault(os.Stdin); err != nil {
				log.Printf("lock: vault: %v", err)
				os.Exit(1)
			}
			os.Exit(0)
		case "lock-process":
			os.Exit(lockProcess())
		}
	}
	testhome.Run(m)
}
