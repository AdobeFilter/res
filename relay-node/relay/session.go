package relay

import (
	"net"
	"sync"
	"time"
)

// Session represents an active relay forwarding session.
type Session struct {
	ID        string
	SrcAddr   *net.UDPAddr
	DstAddr   *net.UDPAddr
	CreatedAt time.Time
	LastSeen  time.Time
	BytesIn   uint64
	BytesOut  uint64
}

// SessionTable manages active relay sessions.
type SessionTable struct {
	mu       sync.RWMutex
	sessions map[string]*Session // keyed by "src->dst"
	capacity int
}

func NewSessionTable(capacity int) *SessionTable {
	return &SessionTable{
		sessions: make(map[string]*Session),
		capacity: capacity,
	}
}

// GetOrCreate retrieves an existing session or creates a new one.
func (st *SessionTable) GetOrCreate(srcAddr, dstAddr *net.UDPAddr) (*Session, bool) {
	key := srcAddr.String() + "->" + dstAddr.String()

	st.mu.RLock()
	if s, ok := st.sessions[key]; ok {
		s.LastSeen = time.Now()
		st.mu.RUnlock()
		return s, false
	}
	st.mu.RUnlock()

	st.mu.Lock()
	defer st.mu.Unlock()

	// Double-check after write lock
	if s, ok := st.sessions[key]; ok {
		s.LastSeen = time.Now()
		return s, false
	}

	if len(st.sessions) >= st.capacity {
		return nil, false
	}

	s := &Session{
		ID:        key,
		SrcAddr:   srcAddr,
		DstAddr:   dstAddr,
		CreatedAt: time.Now(),
		LastSeen:  time.Now(),
	}
	st.sessions[key] = s
	return s, true
}

// Remove deletes a session.
func (st *SessionTable) Remove(key string) {
	st.mu.Lock()
	delete(st.sessions, key)
	st.mu.Unlock()
}

// CleanStale removes sessions not seen within the timeout.
func (st *SessionTable) CleanStale(timeout time.Duration) int {
	st.mu.Lock()
	defer st.mu.Unlock()

	cutoff := time.Now().Add(-timeout)
	removed := 0
	for key, s := range st.sessions {
		if s.LastSeen.Before(cutoff) {
			delete(st.sessions, key)
			removed++
		}
	}
	return removed
}

// Count returns the number of active sessions.
func (st *SessionTable) Count() int {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return len(st.sessions)
}

// All returns a snapshot of all sessions.
func (st *SessionTable) All() []*Session {
	st.mu.RLock()
	defer st.mu.RUnlock()

	result := make([]*Session, 0, len(st.sessions))
	for _, s := range st.sessions {
		result = append(result, s)
	}
	return result
}
