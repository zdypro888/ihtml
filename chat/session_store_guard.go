package chat

import (
	"context"
	"errors"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

var errExternalSessionStore = errors.New("external session store callback failed")

var errExternalSessionStorePayload = errors.New("external session store payload exceeds safety limits")

const (
	maxExternalSessionStoreEvents       = 256
	maxExternalSessionStorePayloadBytes = 4 << 20
	maxExternalSessionStorePayloadNodes = 65536
	maxExternalSessionStorePayloadDepth = 64
	maxExternalSessionStoreCursorBytes  = 256
)

// guardedSessionStore is intentionally per session even when the underlying
// store is shared. That lets LoadEvents and AppendEvents participate in the
// exact owner/Close barrier while the Backend operation limiter bounds broken
// stores globally across arbitrary csids.
type guardedSessionStore struct {
	session *session
	base    adk.SessionEventStore[*schema.Message]
}

func guardExternalSessionStore(s *session, store adk.SessionEventStore[*schema.Message]) adk.SessionEventStore[*schema.Message] {
	if s == nil || store == nil {
		return store
	}
	return &guardedSessionStore{session: s, base: store}
}

func (s *guardedSessionStore) LoadEvents(
	ctx context.Context,
	sessionID string,
	req *adk.LoadSessionEventsRequest,
) (result *adk.LoadSessionEventsResult[*schema.Message], err error) {
	if s == nil || s.base == nil || s.session == nil {
		return nil, errExternalSessionStore
	}
	done, err := s.session.tryBeginOperation()
	if err != nil {
		return nil, err
	}
	defer done()
	defer func() {
		if recover() != nil {
			result = nil
			err = errExternalSessionStore
		}
	}()
	boundedReq := &adk.LoadSessionEventsRequest{Limit: maxExternalSessionStoreEvents}
	if req != nil {
		*boundedReq = *req
		boundedReq.Kinds = append([]adk.SessionEventKind(nil), req.Kinds...)
		if len(boundedReq.After) > maxExternalSessionStoreCursorBytes || len(boundedReq.Kinds) > 64 {
			return nil, errExternalSessionStorePayload
		}
		if boundedReq.Limit <= 0 || boundedReq.Limit > maxExternalSessionStoreEvents {
			boundedReq.Limit = maxExternalSessionStoreEvents
		}
	}
	result, err = s.base.LoadEvents(ctx, sessionID, boundedReq)
	if err != nil || result == nil {
		return result, err
	}
	if len(result.Events) > maxExternalSessionStoreEvents || len(result.Next) > maxExternalSessionStoreCursorBytes ||
		!jsonPayloadWithinLimits(result, maxExternalSessionStorePayloadBytes, maxExternalSessionStorePayloadNodes, maxExternalSessionStorePayloadDepth) {
		return nil, errExternalSessionStorePayload
	}
	return result, nil
}

func (s *guardedSessionStore) AppendEvents(
	ctx context.Context,
	sessionID string,
	events []*adk.SessionEvent[*schema.Message],
) (err error) {
	if s == nil || s.base == nil || s.session == nil {
		return errExternalSessionStore
	}
	done, err := s.session.tryBeginOperation()
	if err != nil {
		return err
	}
	defer done()
	defer func() {
		if recover() != nil {
			err = errExternalSessionStore
		}
	}()
	if len(events) > maxExternalSessionStoreEvents ||
		!jsonPayloadWithinLimits(events, maxExternalSessionStorePayloadBytes, maxExternalSessionStorePayloadNodes, maxExternalSessionStorePayloadDepth) {
		return errExternalSessionStorePayload
	}
	return s.base.AppendEvents(ctx, sessionID, events)
}
