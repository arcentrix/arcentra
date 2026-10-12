package config

import "testing"

func TestExecutionCapacityMatchesSerialWorker(t *testing.T) {
	for _, configured := range []int{0, 1} {
		if got, err := (AgentInfo{MaxConcurrentJobs: configured}).ExecutionCapacity(); err != nil || got != 1 {
			t.Fatalf("configured %d: got %d, %v", configured, got, err)
		}
	}
	for _, configured := range []int{-1, 2, 5} {
		if _, err := (AgentInfo{MaxConcurrentJobs: configured}).ExecutionCapacity(); err == nil {
			t.Fatalf("unsupported capacity %d accepted", configured)
		}
	}
}
