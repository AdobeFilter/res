// Package events implements a tiny in-memory pub/sub keyed by account_id,
// used to wake long-polling heartbeat handlers as soon as something
// account-scoped changes (settings update, peer joined/left, peer's own
// heartbeat refreshed last_seen).
//
// Buffered chan size = 1 with a non-blocking send: pending wakeups are
// coalesced — a subscriber that's still processing the previous wakeup
// doesn't get a queue of duplicates.
package events

import "sync"

// Broker is safe for concurrent use.
type Broker struct {
	mu   sync.Mutex
	subs map[string]map[chan struct{}]struct{}
}

func NewBroker() *Broker {
	return &Broker{subs: make(map[string]map[chan struct{}]struct{})}
}

// Subscribe registers a wake-channel for the given account. The returned
// unsubscribe func must be called (defer) to free the channel from the
// broker's map — otherwise a long-lived client holds the slot forever.
func (b *Broker) Subscribe(accountID string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	b.mu.Lock()
	if b.subs[accountID] == nil {
		b.subs[accountID] = make(map[chan struct{}]struct{})
	}
	b.subs[accountID][ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		if set, ok := b.subs[accountID]; ok {
			delete(set, ch)
			if len(set) == 0 {
				delete(b.subs, accountID)
			}
		}
		b.mu.Unlock()
	}
}

// Publish wakes every subscriber for an account. Non-blocking: subscribers
// that haven't drained their previous wake are skipped (the next read of
// the channel still sees the pending notification).
func (b *Broker) Publish(accountID string) {
	b.mu.Lock()
	set := b.subs[accountID]
	chans := make([]chan struct{}, 0, len(set))
	for ch := range set {
		chans = append(chans, ch)
	}
	b.mu.Unlock()
	for _, ch := range chans {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
