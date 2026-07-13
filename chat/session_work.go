package chat

import (
	"context"
	"errors"
	"io"
	"sync"

	einoModel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

var errOperationCapacity = NewUserFacingError("AI 服务当前繁忙，请稍后重试。")

// beginOperation tracks provider/tool work by real return, not merely by ADK
// cancellation acknowledgement. This matters for third-party implementations
// that ignore ctx: TurnLoop.Wait may finish after detaching their stream, but a
// successor must not overlap the still-running side effect.
func (s *session) beginOperation() func() {
	done, err := s.tryBeginOperation()
	if err != nil {
		return func() {}
	}
	return done
}

func (s *session) tryBeginOperation() (func(), error) {
	if s == nil {
		return func() {}, nil
	}
	var limiter *operationLimiter
	if s.backend != nil {
		limiter = s.backend.operations
	}
	if limiter != nil && !limiter.tryAcquire() {
		return nil, errOperationCapacity
	}
	s.workMu.Lock()
	if s.closed {
		s.workMu.Unlock()
		if limiter != nil {
			limiter.release()
		}
		return nil, errors.New("chat session is closed")
	}
	if s.activeOps == 0 {
		s.opsDone = make(chan struct{})
	}
	s.activeOps++
	s.workMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			s.workMu.Lock()
			if s.activeOps > 0 {
				s.activeOps--
			}
			if s.activeOps == 0 && s.opsDone != nil {
				close(s.opsDone)
				s.opsDone = nil
			}
			s.workMu.Unlock()
			if limiter != nil {
				limiter.release()
			}
			s.drainPendingTurns()
		})
	}, nil
}

func (s *session) waitOperationsIdle() {
	for {
		s.workMu.Lock()
		if s.activeOps == 0 {
			s.workMu.Unlock()
			return
		}
		done := s.opsDone
		s.workMu.Unlock()
		<-done
	}
}

// trackedChatModel covers both the call that creates a stream and the full
// lifetime of that stream. A proxy is necessary because Stream commonly
// returns before the provider has finished producing output.
type trackedChatModel struct {
	base    einoModel.ToolCallingChatModel
	session *session
}

func trackChatModel(s *session, base einoModel.ToolCallingChatModel) einoModel.ToolCallingChatModel {
	if s == nil || base == nil {
		return base
	}
	return &trackedChatModel{base: base, session: s}
}

func (m *trackedChatModel) WithTools(tools []*schema.ToolInfo) (einoModel.ToolCallingChatModel, error) {
	bound, err := m.base.WithTools(tools)
	if err != nil {
		return nil, err
	}
	return trackChatModel(m.session, bound), nil
}

func (m *trackedChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...einoModel.Option) (*schema.Message, error) {
	done, err := m.session.tryBeginOperation()
	if err != nil {
		return nil, err
	}
	defer done()
	return m.base.Generate(ctx, input, opts...)
}

func (m *trackedChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...einoModel.Option) (*schema.StreamReader[*schema.Message], error) {
	done, err := m.session.tryBeginOperation()
	if err != nil {
		return nil, err
	}
	stream, err := m.base.Stream(ctx, input, opts...)
	if err != nil {
		done()
		return nil, err
	}
	if stream == nil {
		done()
		return nil, nil
	}
	reader, writer := schema.Pipe[*schema.Message](1)
	go func() {
		defer done()
		defer stream.Close()
		defer writer.Close()
		for {
			chunk, recvErr := stream.Recv()
			if recvErr != nil {
				if recvErr != io.EOF {
					writer.Send(chunk, recvErr)
				}
				return
			}
			if writer.Send(chunk, nil) {
				return
			}
		}
	}()
	return reader, nil
}
