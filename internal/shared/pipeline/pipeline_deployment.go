package pipeline

import "context"

// DeploymentStepExecution 携带控制面已分配的部署执行标识，不接受 DSL 身份信息
type DeploymentStepExecution struct {
	PipelineRunID string
	PipelineID    string
	JobRunID      string
	StepRunID     string
	JobName       string
	StepName      string
}

// DeploymentStepRunner 仅在持久化发布实际观察成功后返回成功
type DeploymentStepRunner func(context.Context, DeploymentStepExecution) error
