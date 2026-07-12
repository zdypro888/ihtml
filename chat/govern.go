package chat

import (
	"context"
	"errors"
	"sync"
	"time"
)

const defaultChatWritesPerMinute = 240

// ErrWriteRateLimited 表示 AI 写工具触发了 Backend 级写限额。
var ErrWriteRateLimited = errors.New("AI 写操作过于频繁，请稍后再试")

type userWriteLimiter struct {
	mu      sync.Mutex
	limit   int
	windows map[string]writeWindow
	now     func() time.Time
	ops     uint64
}

type writeWindow struct {
	started time.Time
	count   int
}

func newUserWriteLimiter(limit int) *userWriteLimiter {
	if limit == 0 {
		limit = defaultChatWritesPerMinute
	} else if limit < 0 {
		limit = 0
	}
	return &userWriteLimiter{
		limit: limit, windows: make(map[string]writeWindow), now: time.Now,
	}
}

func (l *userWriteLimiter) allow(user string) error {
	if l == nil || l.limit <= 0 {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	window := l.windows[user]
	if window.started.IsZero() || now.Before(window.started) || now.Sub(window.started) >= time.Minute {
		window = writeWindow{started: now}
	}
	if window.count >= l.limit {
		return ErrWriteRateLimited
	}
	window.count++
	l.windows[user] = window
	l.ops++
	if l.ops%256 == 0 {
		for key, candidate := range l.windows {
			if now.Sub(candidate.started) >= 2*time.Minute {
				delete(l.windows, key)
			}
		}
	}
	return nil
}

func (rt *runtime) allowWrite(ctx context.Context, toolName string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if rt.cfg.WriteInterceptor != nil {
		if err := rt.cfg.WriteInterceptor(ctx, rt.user, toolName); err != nil {
			return err
		}
	}
	return rt.writeLimiter.allow(rt.user)
}
