package dm

import (
	"bytes"
	"io"
	"time"
)

const (
	tailLines          = 8
	tailLineBytes      = 512
	greeterOutputDrain = time.Second
)

type greeterExit struct {
	err    error
	output []string
}

func (e *greeterExit) Error() string {
	if e.err == nil {
		return "the greeter ended"
	}
	return "the greeter ended: " + e.err.Error()
}

func (e *greeterExit) Unwrap() error { return e.err }

type outputTail struct {
	out     io.Writer
	partial []byte
	lines   []string
}

func newOutputTail(out io.Writer) *outputTail {
	return &outputTail{out: out, lines: make([]string, 0, tailLines)}
}

func (t *outputTail) Write(p []byte) (int, error) {
	t.partial = append(t.partial, p...)
	end := bytes.LastIndexByte(t.partial, '\n') + 1
	if end == 0 && len(t.partial) < tailLineBytes {
		return len(p), nil
	}
	if end == 0 {
		end = len(t.partial)
	}
	complete := t.partial[:end]
	for line := range bytes.Lines(complete) {
		t.record(line)
	}
	_, err := t.out.Write(complete)
	t.partial = append(t.partial[:0], t.partial[end:]...)
	return len(p), err
}

func (t *outputTail) flush() error {
	if len(t.partial) == 0 {
		return nil
	}
	t.record(t.partial)
	_, err := t.out.Write(append(t.partial, '\n'))
	t.partial = t.partial[:0]
	return err
}

func (t *outputTail) record(line []byte) {
	line = bytes.TrimRight(line, "\r\n")
	if len(line) == 0 {
		return
	}
	if len(line) > tailLineBytes {
		line = line[:tailLineBytes]
	}
	if len(t.lines) == tailLines {
		t.lines = append(t.lines[:0], t.lines[1:]...)
	}
	t.lines = append(t.lines, string(line))
}

func (t *outputTail) last() []string { return append([]string(nil), t.lines...) }
