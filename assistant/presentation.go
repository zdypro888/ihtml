package assistant

import "context"

// PresentationIdentity 是公开展示的可信关联元数据，不是权限、幂等或业务状态依据。
type PresentationIdentity struct {
	FlowID string
	RunID  string
}
type presentationKey struct{}

func withPresentation(ctx context.Context, flow, run string) context.Context {
	return context.WithValue(ctx, presentationKey{}, PresentationIdentity{flow, run})
}

// Presentation 返回当前框架运行关联，宿主可与原始消息分开保存展示版本，不改模型证据。
func Presentation(ctx context.Context) PresentationIdentity {
	v, _ := ctx.Value(presentationKey{}).(PresentationIdentity)
	return v
}
