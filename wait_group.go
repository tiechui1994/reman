package main

import (
	"context"
	"sync"
)

// ReuseWaitGroup is similar to sync.WaitGroup but is intentionally simple and permissive:
// - Add(delta) may be called at any time (before/during/after Wait).
// - Wait() can be called multiple times across "rounds" of work.
// - WaitWithContext(ctx) waits like Wait but can be canceled by context.
// It enforces the same basic correctness rule as sync.WaitGroup: the internal counter
// must not become negative (Add that would make it negative panics).
//
// Implementation notes:
//   - uses a mutex + condition variable to guard the counter and to wake waiters.
//   - when counter reaches zero, all waiters are woken. If later Add increases the counter,
//     subsequent Wait calls will block until the counter reaches zero again.
//   - Add with a positive delta while other goroutines are waiting will keep Wait blocked
//     until the counter returns to zero.
type ReuseWaitGroup struct {
	mu    sync.Mutex
	cond  *sync.Cond
	count int64
}

// NewReuseWaitGroup creates an initialized ReuseWaitGroup.
func NewReuseWaitGroup() *ReuseWaitGroup {
	r := &ReuseWaitGroup{}
	r.cond = sync.NewCond(&r.mu)
	return r
}

// Add adds delta to the internal counter. delta may be positive or negative.
// If the counter becomes zero, any waiters are woken. If the counter would
// become negative, Add panics (same behavior as sync.WaitGroup).
func (r *ReuseWaitGroup) Add(delta int) {
	if delta == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	newCount := r.count + int64(delta)
	if newCount < 0 {
		// Mirror sync.WaitGroup behaviour: panic on negative counter.
		panic("ReuseWaitGroup: negative WaitGroup counter")
	}
	r.count = newCount

	// If we reached zero, wake all waiters.
	if r.count == 0 {
		r.cond.Broadcast()
	}
}

// Done decrements the counter by one. It panics if the counter would go negative.
func (r *ReuseWaitGroup) Done() {
	r.Add(-1)
}

// Wait blocks until the internal counter becomes zero.
func (r *ReuseWaitGroup) Wait() {
	r.mu.Lock()
	for r.count > 0 {
		r.cond.Wait()
	}
	r.mu.Unlock()
}

// WaitWithContext waits until the counter becomes zero or the context is done.
// Returns nil on success, or ctx.Err() if the context was canceled/expired.
func (r *ReuseWaitGroup) WaitWithContext(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		r.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Current returns the current counter (for diagnostics/testing).
func (r *ReuseWaitGroup) Current() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count
}
