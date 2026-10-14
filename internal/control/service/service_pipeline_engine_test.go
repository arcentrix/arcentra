package service

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/safe"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

type pipelineBindingCaptureEngine struct {
	triggerCaptureEngine
	cancelled, paused, resumed int
}

func (e *pipelineBindingCaptureEngine) CancelRun(string) error { e.cancelled++; return nil }
func (e *pipelineBindingCaptureEngine) PauseRun(string) error  { e.paused++; return nil }
func (e *pipelineBindingCaptureEngine) ResumeRun(string) error { e.resumed++; return nil }

func TestPipelineGRPCServiceSeesEngineBoundAfterRegistration(t *testing.T) {
	adapter, repositories, _, _ := pipelineRuntimeFixture(t, pipelineRuntimeSpec())
	services, err := ProvideServices(adapter, nil, repositories, pipelineRuntimePolicy())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = services.LogAggregator.Close(context.Background()) })
	// Reproduce production ordering: register gRPC before bootstrap binds the engine.
	implementation := NewPipelineServiceImpl(services)
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	pipelinev1.RegisterPipelineServiceServer(server, implementation)
	safe.Go(func() { _ = server.Serve(listener) })
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	conn, err := grpc.NewClient("passthrough:///pipeline-binding-test", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := pipelinev1.NewPipelineServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request := &pipelinev1.TriggerPipelineRequest{PipelineId: "runtime-pipeline", RequestId: "late-engine", TriggeredBy: "fixture"}
	unavailable, err := client.TriggerPipeline(ctx, request)
	if err != nil || unavailable.Success || unavailable.GetError().GetCode() != 503 {
		t.Fatalf("unbound engine did not fail closed: %+v %v", unavailable, err)
	}
	if err := services.BindPipelineEngine((*pipelineBindingCaptureEngine)(nil)); err == nil {
		t.Fatal("typed nil engine accepted")
	}
	unavailable, err = client.TriggerPipeline(ctx, request)
	if err != nil || unavailable.Success || unavailable.GetError().GetCode() != 503 {
		t.Fatalf("rejected typed nil changed availability: %+v %v", unavailable, err)
	}
	engine := &pipelineBindingCaptureEngine{}
	if err := services.BindPipelineEngine(engine); err != nil {
		t.Fatal(err)
	}
	if err := services.BindPipelineEngine(&pipelineBindingCaptureEngine{}); err == nil {
		t.Fatal("running engine replacement accepted")
	}
	triggered, err := client.TriggerPipeline(ctx, request)
	if err != nil || !triggered.Success || engine.calls != 1 {
		t.Fatalf("registered service retained nil engine: %+v %v", triggered, err)
	}
	if NewPipelineServiceImpl(services).currentEngine() != engine || implementation.currentEngine() != engine {
		t.Fatal("services constructed before and after binding disagree")
	}
	if err := repositories.Pipeline.UpdateRun(ctx, triggered.RunId, map[string]any{"status": model.PipelineStatusRunning}); err != nil {
		t.Fatal(err)
	}
	paused, err := client.PausePipeline(ctx, &pipelinev1.PausePipelineRequest{PipelineId: "runtime-pipeline", RunId: triggered.RunId})
	if err != nil || !paused.Success || engine.paused != 1 {
		t.Fatalf("pause did not use bound engine: %+v %v", paused, err)
	}
	resumed, err := client.ResumePipeline(ctx, &pipelinev1.ResumePipelineRequest{PipelineId: "runtime-pipeline", RunId: triggered.RunId})
	if err != nil || !resumed.Success || engine.resumed != 1 {
		t.Fatalf("resume did not use bound engine: %+v %v", resumed, err)
	}
	stopped, err := client.StopPipeline(ctx, &pipelinev1.StopPipelineRequest{PipelineId: "runtime-pipeline", RunId: triggered.RunId})
	if err != nil || !stopped.Success || engine.cancelled != 1 {
		t.Fatalf("stop did not use bound engine: %+v %v", stopped, err)
	}
}

func TestPipelineEngineBindingRejectsNilAndConcurrentReplacement(t *testing.T) {
	services := &Services{}
	implementation := NewPipelineServiceImpl(services)
	var missing *Services
	if err := missing.BindPipelineEngine(&pipelineBindingCaptureEngine{}); err == nil {
		t.Fatal("missing services accepted an engine")
	}
	for _, engine := range []IPipelineEngine{nil, (*pipelineBindingCaptureEngine)(nil)} {
		if err := services.BindPipelineEngine(engine); err == nil || implementation.currentEngine() != nil {
			t.Fatalf("nil engine consumed the binding: %v", err)
		}
	}
	engine := &pipelineBindingCaptureEngine{}
	const readers = 16
	var ready, done sync.WaitGroup
	ready.Add(readers)
	done.Add(readers)
	start := make(chan struct{})
	for range readers {
		safe.Go(func() {
			defer done.Done()
			ready.Done()
			<-start
			for range 1000 {
				got := implementation.currentEngine()
				if got != nil && got != engine {
					t.Error("reader observed an unbound engine")
					return
				}
			}
		})
	}
	ready.Wait()
	close(start)
	if err := services.BindPipelineEngine(engine); err != nil {
		t.Fatal(err)
	}
	done.Wait()
	for range readers {
		done.Add(1)
		safe.Go(func() {
			defer done.Done()
			if err := services.BindPipelineEngine(&pipelineBindingCaptureEngine{}); err == nil {
				t.Error("concurrent runtime replacement accepted")
			}
			if implementation.currentEngine() != engine {
				t.Error("replacement changed the original binding")
			}
		})
	}
	done.Wait()
	if err := services.BindPipelineEngine(engine); err == nil {
		t.Fatal("repeated binding of the same engine accepted")
	}
}

func TestPipelineEngineUnavailableForRunControls(t *testing.T) {
	svc := runServiceFixture(t)
	ctx := context.Background()
	stopped, err := svc.StopPipeline(ctx, &pipelinev1.StopPipelineRequest{PipelineId: "pipeline-a", RunId: "run-a"})
	if err != nil || stopped.Success || stopped.GetError().GetCode() != 503 {
		t.Fatalf("stop accepted missing engine: %+v %v", stopped, err)
	}
	paused, err := svc.PausePipeline(ctx, &pipelinev1.PausePipelineRequest{PipelineId: "pipeline-a", RunId: "run-a"})
	if err != nil || paused.Success || paused.GetError().GetCode() != 503 {
		t.Fatalf("pause accepted missing engine: %+v %v", paused, err)
	}
	if err := svc.pipelineRepo.UpdateRun(ctx, "run-a", map[string]any{"status": model.PipelineStatusPaused}); err != nil {
		t.Fatal(err)
	}
	resumed, err := svc.ResumePipeline(ctx, &pipelinev1.ResumePipelineRequest{PipelineId: "pipeline-a", RunId: "run-a"})
	if err != nil || resumed.Success || resumed.GetError().GetCode() != 503 {
		t.Fatalf("resume accepted missing engine: %+v %v", resumed, err)
	}
}
