package grpc

import (
	steprunv1 "github.com/arcentrix/arcentra/api/steprun/v1"
	"github.com/arcentrix/arcentra/internal/case/execution"
)

type StepRunServiceImpl struct {
	steprunv1.UnimplementedStepRunServiceServer
	manageStepRun *execution.ManageStepRunUseCase
}

func NewStepRunServiceImpl(
	manageStepRun *execution.ManageStepRunUseCase,
) *StepRunServiceImpl {
	return &StepRunServiceImpl{
		manageStepRun: manageStepRun,
	}
}
