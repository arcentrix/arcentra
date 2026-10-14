package agentidentity

import "context"

type generationReportKey struct{}

// GenerationReport 为认证报告传递原始代次证据，不自行授予任何权限
type GenerationReport struct {
	ID            string
	BindingDigest string
}

// WithGenerationReport 将认证 RPC 的代次字段传入同一状态事务
func WithGenerationReport(ctx context.Context, id, binding string) context.Context {
	return context.WithValue(ctx, generationReportKey{}, GenerationReport{ID: id, BindingDigest: binding})
}

// GenerationReportFromContext 读取待由数据库验证的代次证据
func GenerationReportFromContext(ctx context.Context) GenerationReport {
	value, _ := ctx.Value(generationReportKey{}).(GenerationReport)
	return value
}
