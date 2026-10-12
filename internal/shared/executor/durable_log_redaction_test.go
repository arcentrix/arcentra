package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arcentrix/arcentra/pkg/logstream"
)

func TestSecretIsRedactedBeforeDurableDiskWrite(t *testing.T) {
	const secret = "private-token-that-crosses-chunks"
	directory := filepath.Join(t.TempDir(), "private-spool")
	publisher, err := NewDurableLogPublisher(context.Background(), managerLogPublisherFunc(func(context.Context, *logstream.BuildLogMessage) error { return nil }), directory, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Close(context.Background())
	manager := NewExecutorManager()
	manager.SetLogPublisher(publisher)
	result, err := manager.ExecuteWithExecutor(context.Background(), &ExecutionRequest{StepRunID: "secret-safe-step", Step: &StepInfo{Name: "split"}, Env: map[string]string{"secrets.token": secret}}, &mockExecutor{name: "split", executeFunc: func(_ context.Context, req *ExecutionRequest) (*ExecutionResult, error) {
		req.OutputHandler("stdout", secret[:13])
		req.OutputHandler("stdout", secret[13:]+"\n")
		return &ExecutionResult{Success: true, Output: secret + "\n"}, nil
	}})
	if err != nil || !result.Success {
		t.Fatalf("execute: %+v %v", result, err)
	}
	files, err := filepath.Glob(filepath.Join(directory, "*.record"))
	if err != nil || len(files) == 0 {
		t.Fatalf("durable records: %v %v", files, err)
	}
	for _, path := range files {
		bytes, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(bytes), secret) || strings.Contains(string(bytes), secret[:13]) || !strings.Contains(string(bytes), "***") {
			t.Fatal("durable record contained raw secret material or omitted masking")
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("private record permission: %v %v", info, err)
		}
	}
}
