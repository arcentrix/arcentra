package grpc

import (
	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/case/pipeline"
)

type PipelineServiceImpl struct {
	pipelinev1.UnimplementedPipelineServiceServer
	managePipeline *pipeline.ManagePipelineUseCase
}

func NewPipelineServiceImpl(
	managePipeline *pipeline.ManagePipelineUseCase,
) *PipelineServiceImpl {
	return &PipelineServiceImpl{
		managePipeline: managePipeline,
	}
}
