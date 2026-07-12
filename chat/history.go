package chat

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

const (
	defaultHistorySessions = 128
	defaultHistoryEvents   = 256
	defaultHistoryBytes    = int64(2 << 20)
	defaultHistoryTTL      = 12 * time.Hour
)

var errHistoryEventTooLarge = errors.New("chat: session history event exceeds the per-session byte limit")

type historyLimits struct {
	maxSessions int
	maxEvents   int
	maxBytes    int64
	idleTTL     time.Duration
}

func historyLimitsFromConfig(cfg Config) historyLimits {
	limits := historyLimits{
		maxSessions: cfg.MaxHistorySessions,
		maxEvents:   cfg.MaxHistoryEvents,
		maxBytes:    cfg.MaxHistoryBytes,
		idleTTL:     cfg.HistoryTTL,
	}
	if limits.maxSessions == 0 {
		limits.maxSessions = defaultHistorySessions
	} else if limits.maxSessions < 0 {
		limits.maxSessions = 0
	}
	if limits.maxEvents == 0 {
		limits.maxEvents = defaultHistoryEvents
	} else if limits.maxEvents < 0 {
		limits.maxEvents = 0
	}
	if limits.maxBytes == 0 {
		limits.maxBytes = defaultHistoryBytes
	} else if limits.maxBytes < 0 {
		limits.maxBytes = 0
	}
	if limits.idleTTL == 0 {
		limits.idleTTL = defaultHistoryTTL
	} else if limits.idleTTL < 0 {
		limits.idleTTL = 0
	}
	return limits
}

// boundedSessionStore 是默认的 ADK 会话历史实现。它按会话数、每会话事件数、
// 每会话序列化字节数和空闲时间淘汰，避免攻击者持续制造 csid 占满内存。
// 活跃 WebSocket 会话会被 pin，关闭后才参与整会话 LRU/TTL 淘汰；单会话内部的
// 事件/字节上限始终生效。
type boundedSessionStore struct {
	mu         sync.Mutex
	limits     historyLimits
	serializer schema.Serializer
	sessions   map[string]*historyEntry
	lru        *list.List // front = 最近使用；元素值为 sessionID
	now        func() time.Time
}

type historyEntry struct {
	events     [][]byte
	eventIDs   []string
	eventKinds []adk.SessionEventKind
	eventIDIdx map[string]int
	bytes      int64
	active     int
	lastUsed   time.Time
	elem       *list.Element
}

type pendingHistoryEvent struct {
	id   string
	kind adk.SessionEventKind
	data []byte
}

func newBoundedSessionStore(limits historyLimits) *boundedSessionStore {
	return &boundedSessionStore{
		limits:     limits,
		serializer: &schema.HumanReadableSerializer{},
		sessions:   make(map[string]*historyEntry),
		lru:        list.New(),
		now:        time.Now,
	}
}

func (s *boundedSessionStore) acquire(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.pruneSessionsLocked(now)
	entry := s.getOrCreateLocked(sessionID, now)
	entry.active++
	s.touchLocked(entry, now)
	s.pruneSessionsLocked(now)
}

func (s *boundedSessionStore) release(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if entry := s.sessions[sessionID]; entry != nil {
		if entry.active > 0 {
			entry.active--
		}
		s.touchLocked(entry, now)
	}
	s.pruneSessionsLocked(now)
}

func (s *boundedSessionStore) AppendEvents(_ context.Context, sessionID string, events []*adk.SessionEvent[*schema.Message]) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	s.pruneSessionsLocked(now)
	entry := s.getOrCreateLocked(sessionID, now)
	seen := make(map[string]struct{}, len(events))
	pending := make([]pendingHistoryEvent, 0, len(events))
	for _, event := range events {
		if event == nil || event.EventID == "" {
			return adk.ErrInvalidEventID
		}
		if _, duplicate := seen[event.EventID]; duplicate {
			return adk.ErrDuplicateEventID
		}
		seen[event.EventID] = struct{}{}
		if _, duplicate := entry.eventIDIdx[event.EventID]; duplicate {
			return adk.ErrDuplicateEventID
		}
		if err := adk.NormalizeSessionEventKind(event); err != nil {
			return err
		}
		data, err := s.serializer.Marshal(event)
		if err != nil {
			return err
		}
		if s.limits.maxBytes > 0 && int64(len(data)) > s.limits.maxBytes {
			return errHistoryEventTooLarge
		}
		pending = append(pending, pendingHistoryEvent{
			id: event.EventID, kind: event.Kind, data: append([]byte(nil), data...),
		})
	}

	for _, event := range pending {
		entry.events = append(entry.events, event.data)
		entry.eventIDs = append(entry.eventIDs, event.id)
		entry.eventKinds = append(entry.eventKinds, event.kind)
		entry.eventIDIdx[event.id] = len(entry.events) - 1
		entry.bytes += int64(len(event.data))
	}
	s.pruneEventsLocked(entry)
	s.touchLocked(entry, now)
	s.pruneSessionsLocked(now)
	return nil
}

func (s *boundedSessionStore) LoadEvents(_ context.Context, sessionID string, req *adk.LoadSessionEventsRequest) (*adk.LoadSessionEventsResult[*schema.Message], error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	s.pruneSessionsLocked(now)
	entry := s.getOrCreateLocked(sessionID, now)
	s.touchLocked(entry, now)
	if req == nil {
		req = &adk.LoadSessionEventsRequest{}
	}
	if req.Reverse {
		return s.loadReverseLocked(entry, req)
	}
	return s.loadForwardLocked(entry, req)
}

func (s *boundedSessionStore) loadForwardLocked(entry *historyEntry, req *adk.LoadSessionEventsRequest) (*adk.LoadSessionEventsResult[*schema.Message], error) {
	start := 0
	if req.After != "" {
		pos, ok := entry.eventIDIdx[req.After]
		if !ok {
			return nil, adk.ErrEventIDOutOfRange
		}
		start = pos + 1
	}
	kinds := historyKindSet(req.Kinds)
	out := make([]*adk.SessionEvent[*schema.Message], 0)
	hasMore := false
	for i := start; i < len(entry.events); i++ {
		if kinds != nil {
			if _, ok := kinds[entry.eventKinds[i]]; !ok {
				continue
			}
		}
		if req.Limit > 0 && len(out) >= req.Limit {
			hasMore = true
			break
		}
		event, err := s.decodeEventLocked(entry, i)
		if err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	next := ""
	if hasMore && len(out) > 0 {
		next = out[len(out)-1].EventID
	}
	return &adk.LoadSessionEventsResult[*schema.Message]{Events: out, Next: next}, nil
}

func (s *boundedSessionStore) loadReverseLocked(entry *historyEntry, req *adk.LoadSessionEventsRequest) (*adk.LoadSessionEventsResult[*schema.Message], error) {
	end := len(entry.events)
	if req.After != "" {
		pos, ok := entry.eventIDIdx[req.After]
		if !ok {
			return nil, adk.ErrEventIDOutOfRange
		}
		end = pos
	}
	kinds := historyKindSet(req.Kinds)
	out := make([]*adk.SessionEvent[*schema.Message], 0)
	hasMore := false
	for i := end - 1; i >= 0; i-- {
		if kinds != nil {
			if _, ok := kinds[entry.eventKinds[i]]; !ok {
				continue
			}
		}
		if req.Limit > 0 && len(out) >= req.Limit {
			hasMore = true
			break
		}
		event, err := s.decodeEventLocked(entry, i)
		if err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	next := ""
	if hasMore && len(out) > 0 {
		next = out[len(out)-1].EventID
	}
	return &adk.LoadSessionEventsResult[*schema.Message]{Events: out, Next: next}, nil
}

func (s *boundedSessionStore) decodeEventLocked(entry *historyEntry, index int) (*adk.SessionEvent[*schema.Message], error) {
	var event adk.SessionEvent[*schema.Message]
	if err := s.serializer.Unmarshal(entry.events[index], &event); err != nil {
		return nil, err
	}
	if err := adk.NormalizeSessionEventKind(&event); err != nil {
		return nil, err
	}
	if event.EventID != entry.eventIDs[index] || event.Kind != entry.eventKinds[index] {
		return nil, fmt.Errorf("chat: session event index mismatch for %q", entry.eventIDs[index])
	}
	return &event, nil
}

func (s *boundedSessionStore) getOrCreateLocked(sessionID string, now time.Time) *historyEntry {
	if entry := s.sessions[sessionID]; entry != nil {
		return entry
	}
	elem := s.lru.PushFront(sessionID)
	entry := &historyEntry{
		eventIDIdx: make(map[string]int),
		lastUsed:   now,
		elem:       elem,
	}
	s.sessions[sessionID] = entry
	return entry
}

func (s *boundedSessionStore) touchLocked(entry *historyEntry, now time.Time) {
	entry.lastUsed = now
	s.lru.MoveToFront(entry.elem)
}

func (s *boundedSessionStore) pruneEventsLocked(entry *historyEntry) {
	drop := 0
	bytes := entry.bytes
	for drop < len(entry.events) {
		overEvents := s.limits.maxEvents > 0 && len(entry.events)-drop > s.limits.maxEvents
		overBytes := s.limits.maxBytes > 0 && bytes > s.limits.maxBytes
		if !overEvents && !overBytes {
			break
		}
		bytes -= int64(len(entry.events[drop]))
		drop++
	}
	if drop == 0 {
		return
	}
	entry.events = append([][]byte(nil), entry.events[drop:]...)
	entry.eventIDs = append([]string(nil), entry.eventIDs[drop:]...)
	entry.eventKinds = append([]adk.SessionEventKind(nil), entry.eventKinds[drop:]...)
	entry.bytes = bytes
	entry.eventIDIdx = make(map[string]int, len(entry.eventIDs))
	for i, id := range entry.eventIDs {
		entry.eventIDIdx[id] = i
	}
}

func (s *boundedSessionStore) pruneSessionsLocked(now time.Time) {
	if s.limits.idleTTL > 0 {
		for elem := s.lru.Back(); elem != nil; {
			prev := elem.Prev()
			entry := s.sessions[elem.Value.(string)]
			if entry != nil && entry.active == 0 && now.Sub(entry.lastUsed) >= s.limits.idleTTL {
				s.removeEntryLocked(elem)
			}
			elem = prev
		}
	}
	for s.limits.maxSessions > 0 && len(s.sessions) > s.limits.maxSessions {
		var victim *list.Element
		for elem := s.lru.Back(); elem != nil; elem = elem.Prev() {
			if entry := s.sessions[elem.Value.(string)]; entry != nil && entry.active == 0 {
				victim = elem
				break
			}
		}
		if victim == nil { // 全部会话仍活跃，关闭时会再次收敛到上限。
			break
		}
		s.removeEntryLocked(victim)
	}
}

func (s *boundedSessionStore) removeEntryLocked(elem *list.Element) {
	delete(s.sessions, elem.Value.(string))
	s.lru.Remove(elem)
}

func historyKindSet(kinds []adk.SessionEventKind) map[adk.SessionEventKind]struct{} {
	if len(kinds) == 0 {
		return nil
	}
	out := make(map[adk.SessionEventKind]struct{}, len(kinds))
	for _, kind := range kinds {
		out[kind] = struct{}{}
	}
	return out
}
