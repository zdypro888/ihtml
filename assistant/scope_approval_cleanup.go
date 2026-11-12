package assistant

import (
	"context"
	"encoding/json"

	"github.com/zdypro888/ihtml/authorization"
)

// revokePendingScope 仅在原确认 nonce 成功撤销后调用；已批准范围不随传输关闭回收。
func (s *session) revokePendingScope(ctx context.Context, item *pendingTool) error {
	if item == nil || s.backend.cfg.ScopeApproval == nil || item.tool.Name != authorizationRequest {
		return nil
	}
	var ref struct {
		ID     string `json:"id"`
		Digest string `json:"digest"`
	}
	if json.Unmarshal(item.args, &ref) != nil || ref.ID == "" {
		return authorization.ErrInvalid
	}
	binding := authorization.Binding{Owner: s.sctx.User, Conversation: item.sessionID, Capability: item.sessionCapability}
	m, err := s.scopeManager(ctx)
	if err != nil {
		return err
	}
	r, err := m.Get(ctx, binding, ref.ID)
	if err != nil {
		return err
	}
	if r.State != authorization.Pending {
		return nil
	}
	if r.Digest != ref.Digest {
		return authorization.ErrDenied
	}
	return m.RevokePending(ctx, binding, ref.ID)
}
