package main

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"testing"

	"golang.org/x/sys/unix"
)

const (
	mainThreadProbeEnv = "LYS_LOGIN_TEST_MAIN_THREAD_MODE"
	exitOnMainThread   = 10
	exitOffMainThread  = 11
)

func probeMainThread(mode string) int {
	releaseMainThreadUnlessWorker(mode)
	for range 8 {
		captured := make(chan struct{})
		go func() {
			runtime.LockOSThread()
			close(captured)
			select {}
		}()
		<-captured
	}
	if unix.Gettid() == unix.Getpid() {
		return exitOnMainThread
	}
	return exitOffMainThread
}

func TestOnlyTheWorkerHoldsTheMainThread(t *testing.T) {
	for _, tc := range []struct {
		mode string
		want int
	}{
		{"worker", exitOnMainThread},
		{"greet", exitOffMainThread},
		{"daemon", exitOffMainThread},
		{"lock", exitOffMainThread},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			cmd := exec.Command(os.Args[0])
			cmd.Env = append(os.Environ(), mainThreadProbeEnv+"="+tc.mode)
			err := cmd.Run()
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				t.Fatalf("run the probe: %v", err)
			}
			if got := exit.ExitCode(); got != tc.want {
				t.Errorf("the %s's main goroutine exits with %d, want %d", tc.mode, got, tc.want)
			}
		})
	}
}
