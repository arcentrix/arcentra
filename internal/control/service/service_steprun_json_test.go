package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	steprunv1 "github.com/arcentrix/arcentra/api/steprun/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

type stepRunJSONTestRepo struct {
	repo.IStepRunRepository
	row     *model.StepRun
	updates map[string]any
	err     error
}

func (r *stepRunJSONTestRepo) Create(_ context.Context, row *model.StepRun) error {
	r.row = row
	return r.err
}
func (r *stepRunJSONTestRepo) GetByStepRunID(context.Context, string) (*model.StepRun, error) {
	return r.row, r.err
}
func (r *stepRunJSONTestRepo) List(context.Context, repo.StepRunFilter) ([]model.StepRun, int64, error) {
	return []model.StepRun{*r.row}, 1, r.err
}
func (r *stepRunJSONTestRepo) PatchByStepRunID(_ context.Context, _ string, updates map[string]any) error {
	r.updates = updates
	return r.err
}

func TestStepRunJSONSerializationAndPatchPresence(t *testing.T) {
	ctx := context.Background()
	r := &stepRunJSONTestRepo{}
	s := NewStepRunServiceImpl(r)
	req := &steprunv1.CreateStepRunRequest{PipelineId: "pipeline", JobId: "job", StepName: "step"}
	resp, err := s.CreateStepRun(ctx, req)
	if err != nil || !resp.Success {
		t.Fatalf("empty create: %v %v", resp, err)
	}
	for field, value := range map[string]string{"args": r.row.Args, "env": r.row.Env, "secrets": r.row.Secrets, "selector": r.row.LabelSelector} {
		if !json.Valid([]byte(value)) || value != "{}" {
			t.Fatalf("%s = %q", field, value)
		}
	}
	args, err := structpb.NewStruct(map[string]any{"command": "echo hello", "nested": []any{true, "value"}})
	if err != nil {
		t.Fatal(err)
	}
	req.Args, req.Env, req.Secrets = args, map[string]string{"ENV": "value"}, map[string]string{"TOKEN": "write-only-canary"}
	resp, err = s.CreateStepRun(ctx, req)
	if err != nil || !resp.Success {
		t.Fatalf("populated create: %v %v", resp, err)
	}
	if !strings.Contains(r.row.Secrets, "write-only-canary") {
		t.Fatal("write-only secret was not stored")
	}
	if _, err := s.UpdateStepRun(ctx, &steprunv1.UpdateStepRunRequest{StepRunId: "step", Timeout: "1m"}); err != nil {
		t.Fatal(err)
	}
	if _, supplied := r.updates["env"]; supplied {
		t.Fatal("omitted env became a destructive patch")
	}
	if _, err := s.UpdateStepRun(ctx, &steprunv1.UpdateStepRunRequest{StepRunId: "step", Env: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	if r.updates["env"] != "{}" {
		t.Fatal("explicit empty map must be a valid empty object")
	}
}

func TestStepRunManagementResponsesNeverExposeSecrets(t *testing.T) {
	ctx := context.Background()
	canary := "step-secret-response-canary"
	r := &stepRunJSONTestRepo{row: &model.StepRun{StepRunID: "step", Secrets: `{"TOKEN":"` + canary + `"}`}}
	s := NewStepRunServiceImpl(r)
	get, err := s.GetStepRun(ctx, &steprunv1.GetStepRunRequest{StepRunId: "step"})
	if err != nil || !get.Success {
		t.Fatalf("get: %v %v", get, err)
	}
	list, err := s.ListStepRuns(ctx, &steprunv1.ListStepRunsRequest{})
	if err != nil || !list.Success {
		t.Fatalf("list: %v %v", list, err)
	}
	for _, response := range []proto.Message{get, list} {
		encoded, err := protojson.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), canary) || strings.Contains(string(encoded), "TOKEN") {
			t.Fatal("step management response exposed secret")
		}
	}
	if len(get.StepRun.Metrics) != 0 || len(list.StepRuns[0].Metrics) != 0 {
		t.Fatal("secrets returned as metrics")
	}
	// Driver errors may contain attempted values; management failures must
	// not echo raw persistence errors into responses or service logs.
	r.err = errors.New("failed write: " + canary)
	failedCreate, _ := s.CreateStepRun(ctx, &steprunv1.CreateStepRunRequest{PipelineId: "pipeline", JobId: "job", StepName: "step", Secrets: map[string]string{"TOKEN": canary}})
	failedUpdate, _ := s.UpdateStepRun(ctx, &steprunv1.UpdateStepRunRequest{StepRunId: "step", Env: map[string]string{"TOKEN": canary}})
	failedGet, _ := s.GetStepRun(ctx, &steprunv1.GetStepRunRequest{StepRunId: "step"})
	failedList, _ := s.ListStepRuns(ctx, &steprunv1.ListStepRunsRequest{})
	for _, response := range []proto.Message{failedCreate, failedUpdate, failedGet, failedList} {
		encoded, err := protojson.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), canary) {
			t.Fatal("persistence error exposed secret")
		}
	}
}

func TestStepRunEncodingFailureDoesNotWrite(t *testing.T) {
	args := &structpb.Struct{Fields: map[string]*structpb.Value{"private": structpb.NewStringValue("encoding-secret-canary")}}
	for i := 0; i < 6000; i++ {
		args = &structpb.Struct{Fields: map[string]*structpb.Value{"nested": structpb.NewStructValue(args)}}
	}
	if _, err := marshalStepRunStruct(args); err == nil {
		t.Fatal("expected JSON encoder nesting-depth error")
	}
	r := &stepRunJSONTestRepo{}
	response, err := NewStepRunServiceImpl(r).CreateStepRun(context.Background(), &steprunv1.CreateStepRunRequest{PipelineId: "pipeline", JobId: "job", StepName: "step", Args: args, Secrets: map[string]string{"TOKEN": "encoding-secret-canary"}})
	if err != nil || response.Success || response.Message != "encode step run args failed" {
		t.Fatalf("encoding failure was not propagated safely: %v %v", response, err)
	}
	if r.row != nil || r.updates != nil {
		t.Fatal("encoding failure wrote partial data")
	}
}
