package agentidentity

import "context"

// Identity 表示经过认证的 Agent 身份
type Identity struct {
	ID    string
	Roles []string
}
type contextKey struct{}

// WithIdentity 将认证身份加入请求上下文
func WithIdentity(ctx context.Context, identity Identity) context.Context {
	return context.WithValue(ctx, contextKey{}, identity)
}

// FromContext 读取请求经过认证的身份
func FromContext(ctx context.Context) (*Identity, bool) {
	value, ok := ctx.Value(contextKey{}).(Identity)
	if !ok {
		return nil, false
	}
	return &value, true
}
