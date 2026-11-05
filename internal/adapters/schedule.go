package adapters

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Every request to an instance, from any part of Fedigest, takes its turn
// in that instance's queue, and turns are at least HostSpacing apart: one
// request a second per instance in total. Requests with normal priority
// (visitors, admins, auto-ingest of new posts) go before background ones
// (the comment-count job, the metadata refresh); within a priority, first
// come first served. A request that would have to wait past its own
// deadline gives up at once with a PausedError (Busy), so callers treat it
// like a pause rather than a failure.

// HostSpacing is the least time between two requests to the same instance.
// main sets it; tests leave it at 0 (no queue).
var HostSpacing time.Duration

// Priority orders the requests waiting for the same instance.
type Priority int

const (
	PriorityNormal     Priority = iota // visitors, admins, auto-ingest
	PriorityBackground                 // comment-count job, metadata refresh
)

type priorityKey struct{}

// Background marks requests made with ctx as background work: they wait
// behind everything else for the same instance.
func Background(ctx context.Context) context.Context {
	return context.WithValue(ctx, priorityKey{}, PriorityBackground)
}

func priorityOf(ctx context.Context) Priority {
	if p, ok := ctx.Value(priorityKey{}).(Priority); ok {
		return p
	}
	return PriorityNormal
}

// requestMargin is time left for the request itself after its turn comes:
// a request whose turn would come later than its deadline minus this
// doesn't queue.
const requestMargin = 3 * time.Second

type turnWaiter struct {
	prio  Priority
	seq   uint64
	ready chan struct{}
}

type hostTurns struct {
	next    time.Time     // when the next request may go
	waiting []*turnWaiter // by priority, then arrival
	timer   *time.Timer
}

var turns = struct {
	sync.Mutex
	hosts map[string]*hostTurns
	seq   uint64
}{hosts: map[string]*hostTurns{}}

// waitTurn blocks until it's this request's turn at host.
func waitTurn(ctx context.Context, host string) error {
	if HostSpacing <= 0 {
		return nil
	}
	key := hostKey(host)
	turns.Lock()
	now := time.Now()
	h := turns.hosts[key]
	if h == nil {
		pruneIdleHosts(now)
		h = &hostTurns{}
		turns.hosts[key] = h
	}
	if len(h.waiting) == 0 && !h.next.After(now) {
		h.next = now.Add(HostSpacing)
		turns.Unlock()
		return nil
	}
	turns.seq++
	w := &turnWaiter{prio: priorityOf(ctx), seq: turns.seq, ready: make(chan struct{})}
	pos := sort.Search(len(h.waiting), func(i int) bool {
		o := h.waiting[i]
		return o.prio > w.prio || (o.prio == w.prio && o.seq > w.seq)
	})
	wait := time.Duration(pos)*HostSpacing + max(h.next.Sub(now), 0)
	if dl, ok := ctx.Deadline(); ok && now.Add(wait).After(dl.Add(-requestMargin)) {
		turns.Unlock()
		return &PausedError{Host: key, Left: wait, Busy: true}
	}
	h.waiting = append(h.waiting, nil)
	copy(h.waiting[pos+1:], h.waiting[pos:])
	h.waiting[pos] = w
	h.arm()
	turns.Unlock()

	select {
	case <-w.ready:
		return nil
	case <-ctx.Done():
		turns.Lock()
		for i, o := range h.waiting {
			if o == w {
				h.waiting = append(h.waiting[:i], h.waiting[i+1:]...)
				break
			}
		}
		turns.Unlock()
		return ctx.Err()
	}
}

// arm makes sure a timer will hand out the next turn. Called with the lock.
func (h *hostTurns) arm() {
	if h.timer != nil || len(h.waiting) == 0 {
		return
	}
	h.timer = time.AfterFunc(max(time.Until(h.next), 0), func() {
		turns.Lock()
		defer turns.Unlock()
		h.timer = nil
		if len(h.waiting) == 0 {
			return
		}
		if now := time.Now(); !h.next.After(now) {
			w := h.waiting[0]
			h.waiting = h.waiting[1:]
			h.next = now.Add(HostSpacing)
			close(w.ready)
		}
		h.arm()
	})
}

// pruneIdleHosts forgets instances nobody has asked for a while. Called
// with the lock.
func pruneIdleHosts(now time.Time) {
	for k, h := range turns.hosts {
		if len(h.waiting) == 0 && h.timer == nil && now.Sub(h.next) > time.Minute {
			delete(turns.hosts, k)
		}
	}
}
