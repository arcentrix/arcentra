package grpc

import (
	streamv1 "github.com/arcentrix/arcentra/api/stream/v1"
	"github.com/arcentrix/arcentra/internal/case/execution"
)

type StreamServiceImpl struct {
	streamv1.UnimplementedStreamServiceServer
	manageStepRun *execution.ManageStepRunUseCase
}

func NewStreamServiceImpl(
	manageStepRun *execution.ManageStepRunUseCase,
) *StreamServiceImpl {
	return &StreamServiceImpl{
		manageStepRun: manageStepRun,
	}
}
