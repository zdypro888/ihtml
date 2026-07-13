package chat

// Real provider and Service calls can come from third-party implementations
// that disregard context cancellation. Per-session activeOps preserves
// takeover ordering; this Backend-wide ceiling separately prevents an attacker
// from rotating csids to retain an unbounded number of permanently blocked
// model/tool goroutines.
const defaultMaxOutstandingOperations = 64

type operationLimiter struct {
	slots chan struct{}
}

func newOperationLimiter(limit int) *operationLimiter {
	if limit <= 0 {
		limit = defaultMaxOutstandingOperations
	}
	return &operationLimiter{slots: make(chan struct{}, limit)}
}

func (l *operationLimiter) tryAcquire() bool {
	if l == nil {
		return true
	}
	select {
	case l.slots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (l *operationLimiter) release() {
	if l != nil {
		<-l.slots
	}
}
