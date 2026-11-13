package dm

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/stubbedev/lys-lib/sockdir"

	"github.com/stubbedev/lys-login/internal/keyboard/keyboardtest"
)

func TestTheOutputTailForwardsWholeLinesAndKeepsTheLast(t *testing.T) {
	var forwarded strings.Builder
	tail := newOutputTail(&forwarded)
	for i := range tailLines + 2 {
		if _, err := tail.Write([]byte("line " + string(rune('a'+i)) + "\r\nnext")); err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(forwarded.String(), "next") {
			t.Fatalf("a partial line was forwarded: %q", forwarded.String())
		}
		if _, err := tail.Write([]byte(" part\n")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tail.Write([]byte("unterminated")); err != nil {
		t.Fatal(err)
	}
	if err := tail.flush(); err != nil {
		t.Fatal(err)
	}
	got := tail.last()
	if len(got) != tailLines || got[len(got)-1] != "unterminated" || got[len(got)-2] != "next part" || got[0] != "next part" {
		t.Errorf("tail %q, want the last %d lines ending in the unterminated one", got, tailLines)
	}
	if !strings.HasSuffix(forwarded.String(), "next part\nunterminated\n") || !strings.HasPrefix(forwarded.String(), "line a\r\nnext part\n") {
		t.Errorf("forwarded %q, want every byte in order, the last line ended", forwarded.String())
	}
}

func TestTheOutputTailBoundsALineWithoutAnEnd(t *testing.T) {
	var forwarded strings.Builder
	tail := newOutputTail(&forwarded)
	long := strings.Repeat("x", 3*tailLineBytes)
	if _, err := tail.Write([]byte(long)); err != nil {
		t.Fatal(err)
	}
	if forwarded.String() != long {
		t.Errorf("forwarded %d bytes of an overlong line, want all %d at once", forwarded.Len(), len(long))
	}
	if got := tail.last(); len(got) != 1 || len(got[0]) != tailLineBytes {
		t.Errorf("tail holds %d lines, the first %d bytes, want one of %d", len(got), len(got[0]), tailLineBytes)
	}
}

func TestAGreeterThatEndsCarriesItsLastOutput(t *testing.T) {
	keyboardtest.NoSystemBus(t)
	dir := t.TempDir()
	t.Setenv(fakeDirEnv, dir)
	t.Setenv(fakeGreeterFailEnv, "1")
	o := fakeOptions(dir, sockdir.Dir(t))
	_, err := o.greet(context.Background(), o.VT, nil, 0, func(string) bool { return false })
	exit, ok := errors.AsType[*greeterExit](err)
	if !ok {
		t.Fatalf("greet = %v, want the greeter's exit", err)
	}
	if exit.Error() != "the greeter ended: exit status 1" {
		t.Errorf("error %q, want one line naming the exit status", exit.Error())
	}
	want := []string{"xwayland: :0 is another user's: remove /tmp/.X0-lock: permission denied", "lys-login worker greeter: the greeter's compositor exited: exit status 1"}
	if !slices.Equal(exit.output, want) {
		t.Errorf("output %q, want %q", exit.output, want)
	}
}
