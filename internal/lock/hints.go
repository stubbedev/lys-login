package lock

import (
	"log"
	"sync"
	"time"
)

// hintDrainWait bounds how long the process stays for a hint logind does not
// answer: the session is unlocked by then and logind is only being told.
const hintDrainWait = 5 * time.Second

// hintWorker reports the locked state to logind on its own goroutine, in order,
// since the call is a synchronous D-Bus round trip the UI loop must not wait on.
// Only the latest state matters, so states that arrive while one is being sent
// collapse into it.
type hintWorker struct {
	send func(locked bool)
	wake chan struct{}
	done chan struct{}

	mu      sync.Mutex
	latest  bool
	pending bool
}

func newHintWorker(send func(locked bool)) *hintWorker {
	w := &hintWorker{send: send, wake: make(chan struct{}, 1), done: make(chan struct{})}
	go w.run()
	return w
}

// set asks for locked to be reported, and returns at once.
func (w *hintWorker) set(locked bool) {
	w.mu.Lock()
	w.latest, w.pending = locked, true
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *hintWorker) run() {
	defer close(w.done)
	for range w.wake {
		w.flush()
	}
	w.flush()
}

func (w *hintWorker) flush() {
	for {
		w.mu.Lock()
		locked, pending := w.latest, w.pending
		w.pending = false
		w.mu.Unlock()
		if !pending {
			return
		}
		w.send(locked)
	}
}

// drain lets what is pending reach logind and ends the worker, giving up when
// timeout fires. No set may follow it.
func (w *hintWorker) drain(timeout <-chan time.Time) {
	close(w.wake)
	select {
	case <-w.done:
	case <-timeout:
		log.Printf("lock: logind has not answered the locked hint; leaving without it")
	}
}
