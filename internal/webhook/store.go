package webhook

import (
	"context"
	"sync"
	"time"
)

// EventStore tracks which Stripe events have been processed so a retried
// delivery doesn't run your side effects twice.
//
// Claim must be atomic: when two deliveries of the same event race, exactly
// one caller gets true. In production, back this with a database table that
// has a unique constraint on the event ID (see the README).
type EventStore interface {
	// Claim marks an event as in progress. It returns false if the event was
	// already processed or another delivery is handling it right now. A claim
	// that's been in progress too long (the process probably died) can be
	// taken over, so implementations should expire stale claims.
	Claim(ctx context.Context, eventID string) (bool, error)
	// Complete marks a claimed event as processed for good.
	Complete(ctx context.Context, eventID string) error
	// Release gives up a claim after a failure so Stripe's retry can try again.
	Release(ctx context.Context, eventID string) error
}

type eventState int

const (
	stateInProgress eventState = iota
	stateDone
)

type memoryEntry struct {
	state eventState
	at    time.Time
}

// MemoryStore is an in-process EventStore. It's fine for local development
// and tests, but it forgets everything on restart and isn't shared between
// instances, so use a database-backed store in production.
type MemoryStore struct {
	mu      sync.Mutex
	entries map[string]memoryEntry
	// ttl is how long processed event IDs are remembered. Stripe retries for
	// up to three days, so anything shorter than that defeats the purpose.
	ttl time.Duration
	// claimTimeout is how long an in-progress claim blocks other deliveries.
	// If the server crashes mid-event without calling Release, the claim
	// expires and Stripe's retry can be processed instead of being skipped
	// as a duplicate.
	claimTimeout time.Duration
	now          func() time.Time
}

// NewMemoryStore returns a MemoryStore that remembers processed event IDs for
// ttl and lets in-progress claims go stale after claimTimeout. Set
// claimTimeout a little longer than your handler can take.
func NewMemoryStore(ttl, claimTimeout time.Duration) *MemoryStore {
	return &MemoryStore{
		entries:      make(map[string]memoryEntry),
		ttl:          ttl,
		claimTimeout: claimTimeout,
		now:          time.Now,
	}
}

func (s *MemoryStore) Claim(_ context.Context, eventID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evictExpired()

	if e, ok := s.entries[eventID]; ok {
		stale := e.state == stateInProgress && s.claimTimeout > 0 && s.now().Sub(e.at) >= s.claimTimeout
		if !stale {
			return false, nil
		}
	}
	s.entries[eventID] = memoryEntry{state: stateInProgress, at: s.now()}
	return true, nil
}

func (s *MemoryStore) Complete(_ context.Context, eventID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[eventID] = memoryEntry{state: stateDone, at: s.now()}
	return nil
}

func (s *MemoryStore) Release(_ context.Context, eventID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[eventID]; ok && e.state == stateInProgress {
		delete(s.entries, eventID)
	}
	return nil
}

func (s *MemoryStore) evictExpired() {
	if s.ttl <= 0 {
		return
	}
	cutoff := s.now().Add(-s.ttl)
	for id, e := range s.entries {
		if e.at.Before(cutoff) {
			delete(s.entries, id)
		}
	}
}
