package agentidentity

import "context"

type stepAttemptReportKey struct{}

// WithStepAttemptReport 将准确步骤准入摘要传入认证状态事务
func WithStepAttemptReport(ctx context.Context, digest string) context.Context {
	return context.WithValue(ctx, stepAttemptReportKey{}, digest)
}

// StepAttemptReportFromContext 读取待由控制面验证的步骤准入摘要
func StepAttemptReportFromContext(ctx context.Context) string {
	value, _ := ctx.Value(stepAttemptReportKey{}).(string)
	return value
}
