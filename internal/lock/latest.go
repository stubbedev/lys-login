package lock

import (
	"log"
	"sync"
	"time"
)

// drainWait bounds how long the process stays for what it reports on the way
// out: the session is unlocked by then and the reader is only being told.
const drainWait = 5 * time.Second

// latest reports a state to something that can keep the UI loop waiting, logind
// over D-Bus or whoever reads stdout, on its own goroutine and in order. Only the
// latest state matters, so states that arrive while one is being sent collapse
// into it.
type latest[T any] struct {
	what string
	send func(T)
	wake chan struct{}
	done chan struct{}

	mu      sync.Mutex
	value   T
	pending bool
}

func newLatest[T any](what string, send func(T)) *latest[T] {
	w := &latest[T]{what: what, send: send, wake: make(chan struct{}, 1), done: make(chan struct{})}
	go w.run()
	return w
}

// set asks for v to be reported, and returns at once.
func (w *latest[T]) set(v T) {
	w.mu.Lock()
	w.value, w.pending = v, true
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *latest[T]) run() {
	defer close(w.done)
	for range w.wake {
		w.flush()
	}
	w.flush()
}

func (w *latest[T]) flush() {
	for {
		w.mu.Lock()
		v, pending := w.value, w.pending
		w.pending = false
		w.mu.Unlock()
		if !pending {
			return
		}
		w.send(v)
	}
}

// drain lets what is pending through and ends the worker, giving up when timeout
// fires. No set may follow it.
func (w *latest[T]) drain(timeout <-chan time.Time) {
	close(w.wake)
	select {
	case <-w.done:
	case <-timeout:
		log.Printf("lock: %s has not taken the last report; leaving without it", w.what)
	}
}
