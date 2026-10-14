package process

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
)

func (e *Process) selectAgent(ctx context.Context, job *spec.Job) (string, error) {
	return e.selectAgentForRuntime(ctx, job, nil)
}

func (e *Process) selectAgentForRuntime(ctx context.Context, job *spec.Job, runtime *taskqueue.RuntimeRequest) (string, error) {
	if job == nil {
		return "", fmt.Errorf("job is required")
	}
	if err := taskqueue.ValidateRuntimeRequest(runtime, e.offlineRuntimePolicy); err != nil {
		return "", err
	}
	if runtime != nil {
		if err := taskqueue.ValidateRuntimeRetry(job.Retry, e.offlineRuntimePolicy); err != nil {
			return "", err
		}
	}
	if e.repos == nil || e.repos.Agent == nil {
		return "", fmt.Errorf("agent repository is unavailable")
	}
	candidates := make([]string, 0)
	for page := 1; ; page++ {
		agents, total, err := e.repos.Agent.List(ctx, page, 100)
		if err != nil {
			return "", err
		}
		for _, agent := range agents {
			if agent.IsEnabled != 1 || (agent.Status != 1 && agent.Status != 4) || agent.LastHeartbeat == nil || time.Since(*agent.LastHeartbeat) > 2*time.Minute {
				continue
			}
			capabilities, capabilityErr := decodeAgentRuntimeCapabilities(agent.RuntimeCapabilities)
			if capabilityErr != nil {
				continue
			}
			switch capabilities.ExecutionMode {
			case "", taskqueue.ExecutionModeBaremetal:
				if runtime != nil || capabilities.ContainerdOfflineV1 {
					continue
				}
			case taskqueue.RuntimeContainerdOfflineV1:
				if runtime == nil || agent.OS != "linux" || !capabilities.ContainerdOfflineV1 || job.Retry != nil && !capabilities.ContainerdWholeJobRetryV1 {
					continue
				}
			default:
				continue
			}
			labels := map[string]string{}
			if len(agent.Labels) > 0 {
				if err := sonic.Unmarshal(agent.Labels, &labels); err != nil {
					continue
				}
			}
			labels["os"], labels["arch"] = agent.OS, agent.Arch
			matches := true
			for _, step := range job.Steps {
				if step != nil && step.RunOnAgent && !matchesAgentSelector(labels, step.AgentSelector) {
					matches = false
					break
				}
			}
			if matches {
				candidates = append(candidates, agent.AgentID)
			}
		}
		if len(agents) == 0 || int64(page*100) >= total {
			break
		}
	}
	if len(candidates) > 0 {
		return candidates[(e.nextAgent.Add(1)-1)%uint64(len(candidates))], nil
	}
	return "", fmt.Errorf("no enabled online agent matches all job step selectors")
}

func matchesAgentSelector(labels map[string]string, selector *spec.AgentSelector) bool {
	if selector == nil {
		return true
	}
	for key, value := range selector.MatchLabels {
		if v, ok := labels[key]; !ok || v != value {
			return false
		}
	}
	for _, expression := range selector.MatchExpressions {
		if expression == nil {
			return false
		}
		value, exists := labels[expression.Key]
		switch expression.Operator {
		case "In":
			if !exists || !slices.Contains(expression.Values, value) {
				return false
			}
		case "NotIn":
			if exists && slices.Contains(expression.Values, value) {
				return false
			}
		case "Exists":
			if !exists {
				return false
			}
		case "NotExists":
			if exists {
				return false
			}
		case "Gt", "Lt":
			if !exists || len(expression.Values) != 1 {
				return false
			}
			actual, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return false
			}
			expected, err := strconv.ParseFloat(expression.Values[0], 64)
			if err != nil {
				return false
			}
			if expression.Operator == "Gt" && actual <= expected || expression.Operator == "Lt" && actual >= expected {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func decodeAgentRuntimeCapabilities(data []byte) (taskqueue.AgentRuntimeCapabilities, error) {
	var result taskqueue.AgentRuntimeCapabilities
	if len(data) == 0 {
		return result, nil
	}
	var fields map[string]any
	if err := sonic.Unmarshal(data, &fields); err != nil || fields == nil {
		return result, fmt.Errorf("invalid runtime capability report")
	}
	for key, value := range fields {
		switch key {
		case "executionMode":
			mode, ok := value.(string)
			if !ok {
				return result, fmt.Errorf("invalid runtime execution mode")
			}
			result.ExecutionMode = mode
		case "containerdOfflineV1":
			ready, ok := value.(bool)
			if !ok {
				return result, fmt.Errorf("invalid runtime readiness")
			}
			result.ContainerdOfflineV1 = ready
		case "containerdWholeJobRetryV1":
			ready, ok := value.(bool)
			if !ok {
				return result, fmt.Errorf("invalid runtime retry readiness")
			}
			result.ContainerdWholeJobRetryV1 = ready
		default:
			return result, fmt.Errorf("unknown runtime capability field")
		}
	}
	return result, nil
}
