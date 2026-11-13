package dm

import (
	"fmt"
	"strings"
	"time"
	"unicode"
)

const (
	retryPause     = 2 * time.Second
	fastFailure    = 30 * time.Second
	escalateAfter  = 5
	escalatedPause = 5 * time.Minute
)

type clock struct {
	now   func() time.Time
	after func(time.Duration) <-chan time.Time
}

var systemClock = clock{now: time.Now, after: time.After}

type failedRuns int

func (n failedRuns) after(ran time.Duration) failedRuns {
	if ran >= fastFailure {
		return 0
	}
	return n + 1
}

func (n failedRuns) escalated() bool { return n >= escalateAfter }

func (n failedRuns) pause() time.Duration {
	switch {
	case n.escalated():
		return escalatedPause
	case n == 0:
		return retryPause
	}
	return retryPause << (n - 1)
}

func failureNotice(n failedRuns, pause time.Duration, err error, output []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "lys-login: the login screen failed %d times in a row, each within %s of its start.\n", n, fastFailure)
	fmt.Fprintf(&b, "It starts again in %s. The last failure: %s\n", pause, consoleLine(err.Error()))
	if len(output) == 0 {
		return b.String()
	}
	b.WriteString("\nIts last output:\n")
	for _, line := range output {
		b.WriteString("  " + consoleLine(line) + "\n")
	}
	return b.String()
}

func consoleLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) || r == unicode.ReplacementChar {
			return '?'
		}
		return r
	}, strings.ToValidUTF8(s, "?"))
}
