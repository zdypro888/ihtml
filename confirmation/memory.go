package confirmation

import (
	"context"
	"errors"
	"sync"
	"time"
)

const memoryNonceCleanupInterval = time.Minute

// MemoryNonceStore is bounded, process-local, and safe for concurrent use.
// Construct with NewMemoryNonceStore; the zero value fails closed.
type MemoryNonceStore struct {
	mu          sync.Mutex
	nonces      map[string]time.Time
	maxNonces   int
	nextCleanup time.Time
}

// NewMemoryNonceStore uses the legacy default when the optional limit is not
// positive. NewChecked validates configured limits before using this helper.
func NewMemoryNonceStore(maxNonces ...int) *MemoryNonceStore {
	limit := DefaultMaxMemoryNonces
	if len(maxNonces) > 0 && maxNonces[0] > 0 {
		limit = maxNonces[0]
	}
	return &MemoryNonceStore{nonces: make(map[string]time.Time), maxNonces: limit}
}

func (s *MemoryNonceStore) cleanupExpiredLocked(now time.Time) {
	for key, expiry := range s.nonces {
		if !expiry.After(now) {
			delete(s.nonces, key)
		}
	}
	s.nextCleanup = now.Add(memoryNonceCleanupInterval)
}

func (s *MemoryNonceStore) Put(ctx context.Context, nonce string, expiresAt time.Time) error {
	if ctx == nil {
		return errors.New("confirmation context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil {
		return errors.New("confirmation nonce store is unavailable")
	}
	if nonce == "" {
		return errors.New("confirmation nonce is required")
	}
	now := time.Now()
	if !expiresAt.After(now) {
		return errors.New("confirmation nonce expiry must be in the future")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.nonces == nil || s.maxNonces <= 0 {
		return errors.New("confirmation nonce store is unavailable")
	}
	if _, exists := s.nonces[nonce]; exists {
		return errors.New("confirmation nonce already exists")
	}
	// Preserve throttled cleanup: full stores never evict valid cards.
	if s.nextCleanup.IsZero() || !now.Before(s.nextCleanup) {
		s.cleanupExpiredLocked(now)
	}
	if len(s.nonces) >= s.maxNonces {
		return errors.New("confirmation nonce store capacity exceeded")
	}
	s.nonces[nonce] = expiresAt
	return nil
}

func (s *MemoryNonceStore) Consume(ctx context.Context, nonce string, now time.Time) (bool, error) {
	if ctx == nil {
		return false, errors.New("confirmation context is required")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if s == nil {
		return false, errors.New("confirmation nonce store is unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if s.nonces == nil || s.maxNonces <= 0 {
		return false, errors.New("confirmation nonce store is unavailable")
	}
	expiresAt, ok := s.nonces[nonce]
	delete(s.nonces, nonce)
	return ok && expiresAt.After(now), nil
}
