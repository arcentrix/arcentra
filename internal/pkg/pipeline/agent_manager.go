// Copyright 2025 Arcentra Team
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	steprunv1 "github.com/arcentrix/arcentra/api/steprun/v1"
	"github.com/arcentrix/arcentra/internal/pkg/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/log"
	"google.golang.org/protobuf/types/known/structpb"
)

// AgentManager manages agent selection and step run assignment for pipeline execution
type AgentManager struct {
	agentClient   agentv1.AgentServiceClient
	stepRunClient steprunv1.StepRunServiceClient
	logger        log.Logger
	// Agent status cache (updated from heartbeat)
	agentStatusCache map[string]*AgentStatus
	statusCacheMu    sync.RWMutex
}

// NewAgentManager creates a new agent manager
func NewAgentManager(agentClient agentv1.AgentServiceClient, stepRunClient steprunv1.StepRunServiceClient, logger log.Logger) *AgentManager {
	return &AgentManager{
		agentClient:      agentClient,
		stepRunClient:    stepRunClient,
		logger:           logger,
		agentStatusCache: make(map[string]*AgentStatus),
	}
}

// StepExecutionRequest represents a request to execute a step on an agent
type StepExecutionRequest struct {
	PipelineID string
	BuildID    string
	JobName    string
	StepName   string
	Step       *spec.Step
	StepIndex  int // Index of step in job (0-based), used for stage calculation
	Workspace  string
	Env        map[string]string
	Selector   *spec.AgentSelector
	Context    *ExecutionContext // Execution context for variable resolution
}

// StepExecutionResult represents the result of step execution
type StepExecutionResult struct {
	Success   bool
	ExitCode  int32
	Error     string
	StartTime time.Time
	EndTime   time.Time
	Metrics   map[string]string
}

// ExecuteStepOnAgent executes a pipeline step on a selected agent
func (am *AgentManager) ExecuteStepOnAgent(ctx context.Context, req *StepExecutionRequest) (*StepExecutionResult, error) {
	if am.agentClient == nil {
		return nil, fmt.Errorf("agent client is not initialized")
	}

	// Select an agent based on selector
	agentID, err := am.SelectAgent(ctx, req.Selector)
	if err != nil {
		return nil, fmt.Errorf("select agent: %w", err)
	}

	if am.logger.Log != nil {
		am.logger.Log.Infow("executing step on agent", "job", req.JobName, "step", req.StepName, "agent", agentID)
	}

	// Convert step to agent step run
	agentStepRun, err := am.convertStepToStepRun(req)
	if err != nil {
		return nil, fmt.Errorf("convert step to step run: %w", err)
	}

	stepRunID, err := am.createStepRun(ctx, req, agentStepRun)
	if err != nil {
		return nil, fmt.Errorf("create step run: %w", err)
	}

	if am.logger.Log != nil {
		am.logger.Log.Infow("created step run for step, waiting for agent to fetch", "stepRun", stepRunID, "job", req.JobName, "step", req.StepName)
	}

	// 2-5. 等待 agent 拉取步骤执行并执行，监听状态更新
	timeout := 1 * time.Hour // Default timeout
	if agentStepRun.Timeout != "" {
		if duration, err := time.ParseDuration(agentStepRun.Timeout); err == nil {
			timeout = duration
		}
	}

	result, err := am.WaitForStepRunCompletion(ctx, stepRunID, timeout)
	if err != nil {
		return nil, fmt.Errorf("wait for step run completion: %w", err)
	}

	return result, nil
}

// SelectAgent selects an available agent based on the selector criteria
func (am *AgentManager) SelectAgent(ctx context.Context, selector *spec.AgentSelector) (string, error) {
	// Get all available agents from cache
	am.statusCacheMu.RLock()
	availableAgents := make([]*AgentStatus, 0, len(am.agentStatusCache))
	for _, status := range am.agentStatusCache {
		// Filter out offline agents and agents with stale heartbeats
		if time.Since(status.LastHeartbeat) > 5*time.Minute {
			continue
		}
		// Only consider online or idle agents
		if status.Status == "online" || status.Status == "idle" {
			availableAgents = append(availableAgents, status)
		}
	}
	am.statusCacheMu.RUnlock()

	if len(availableAgents) == 0 {
		return "", fmt.Errorf("no available agents found")
	}

	// If no selector specified, return the first available agent
	if selector == nil {
		return availableAgents[0].AgentID, nil
	}

	// Filter agents by label selector
	matchedAgents := make([]*AgentStatus, 0)
	for _, agent := range availableAgents {
		if am.matchAgentLabels(agent, selector) {
			matchedAgents = append(matchedAgents, agent)
		}
	}

	if len(matchedAgents) == 0 {
		return "", fmt.Errorf("no agents match the selector criteria")
	}

	// Select agent with least running step runs (load balancing)
	selectedAgent := matchedAgents[0]
	for _, agent := range matchedAgents[1:] {
		if agent.RunningStepRunsCount < selectedAgent.RunningStepRunsCount {
			selectedAgent = agent
		}
	}

	return selectedAgent.AgentID, nil
}

// matchAgentLabels checks if an agent matches the label selector criteria
func (am *AgentManager) matchAgentLabels(agent *AgentStatus, selector *spec.AgentSelector) bool {
	agentLabels := agent.Labels

	// If selector requires labels but agent has no labels, it doesn't match
	// Note: Agent labels should be loaded from database or provided during registration
	// For now, if agent.Labels is nil/empty and selector requires labels, skip this agent
	if len(selector.MatchLabels) > 0 || len(selector.MatchExpressions) > 0 {
		if len(agentLabels) == 0 {
			// Agent labels not available in cache, cannot match
			// TODO: Query agent labels from database if needed
			if am.logger.Log != nil {
				am.logger.Log.Debugw("agent has no labels in cache, skipping label match",
					"agent", agent.AgentID)
			}
			return false
		}
	}

	// Check matchLabels (all must match)
	if len(selector.MatchLabels) > 0 {
		for key, value := range selector.MatchLabels {
			if agentValue, ok := agentLabels[key]; !ok || agentValue != value {
				return false
			}
		}
	}

	// Check matchExpressions (all must match)
	if len(selector.MatchExpressions) > 0 {
		for _, expr := range selector.MatchExpressions {
			if !am.matchExpression(agentLabels, expr) {
				return false
			}
		}
	}

	return true
}

// matchExpression checks if agent labels match a single expression
func (am *AgentManager) matchExpression(agentLabels map[string]string, expr spec.LabelExpression) bool {
	agentValue, exists := agentLabels[expr.Key]

	switch expr.Operator {
	case "In":
		if !exists {
			return false
		}
		return slices.Contains(expr.Values, agentValue)

	case "NotIn":
		if !exists {
			return true
		}
		return !slices.Contains(expr.Values, agentValue)

	case "Exists":
		return exists

	case "NotExists":
		return !exists

	case "Gt":
		if !exists {
			return false
		}
		// Simple numeric comparison (can be enhanced)
		if len(expr.Values) > 0 {
			// TODO: Implement proper numeric comparison
			// For now, just check if value exists
			return true
		}
		return false

	case "Lt":
		if !exists {
			return false
		}
		// Simple numeric comparison (can be enhanced)
		if len(expr.Values) > 0 {
			// TODO: Implement proper numeric comparison
			// For now, just check if value exists
			return true
		}
		return false

	default:
		return false
	}
}

// convertStepToStepRun converts a pipeline step to an agent step run
func (am *AgentManager) convertStepToStepRun(req *StepExecutionRequest) (*agentv1.StepRun, error) {
	// Resolve variables in step args if context is provided
	var resolvedArgs map[string]any
	if req.Context != nil && len(req.Step.Args) > 0 {
		resolvedArgs = req.Context.ResolveVariables(req.Step.Args)
	} else {
		resolvedArgs = req.Step.Args
	}

	// Serialize args to JSON for passing via environment variable
	var argsJSON []byte
	var err error
	if len(resolvedArgs) > 0 {
		argsJSON, err = json.Marshal(resolvedArgs)
		if err != nil {
			return nil, fmt.Errorf("marshal step args: %w", err)
		}
	}

	// Build commands from step action (not used directly in StepRun, but kept for compatibility)
	if req.Context != nil {
		_, err = am.buildCommands(req.Step, req.Context)
		if err != nil {
			return nil, fmt.Errorf("build commands: %w", err)
		}
	}

	// Prepare environment variables (include plugin params)
	env := make(map[string]string)
	for k, v := range req.Env {
		env[k] = v
	}
	// Add plugin params as environment variable for agent to use
	if len(argsJSON) > 0 {
		env["PLUGIN_PARAMS"] = string(argsJSON)
	}
	// Add plugin action and name for agent reference
	env["PLUGIN_NAME"] = req.Step.Uses
	if req.Step.Action != "" {
		env["PLUGIN_ACTION"] = req.Step.Action
	} else {
		env["PLUGIN_ACTION"] = "Execute"
	}

	// Build label selector (for agent matching, will be converted to steprun format in createStepRun)
	labelSelector := am.buildLabelSelector(req.Selector)

	// Build plugin info list (for agent, will be converted to steprun format in createStepRun)
	plugins := []*agentv1.PluginInfo{
		{
			PluginId: req.Step.Uses,
			Name:     req.Step.Uses,
			// Version and other fields would be populated from plugin registry
		},
	}

	// Parse timeout
	timeout := int32(3600) // Default 1 hour
	if req.Step.Timeout != "" {
		if duration, err := time.ParseDuration(req.Step.Timeout); err == nil {
			timeout = int32(duration.Seconds())
		}
	}

	// Convert args to Struct
	var argsStruct *structpb.Struct
	if len(resolvedArgs) > 0 {
		argsBytes, _ := json.Marshal(resolvedArgs)
		argsStruct = &structpb.Struct{}
		if err := json.Unmarshal(argsBytes, argsStruct); err != nil {
			return nil, fmt.Errorf("convert args to struct: %w", err)
		}
	}

	stepRun := &agentv1.StepRun{
		StepRunId:     fmt.Sprintf("%s-%s-%s", req.PipelineID, req.JobName, req.StepName),
		PipelineId:    req.PipelineID,
		JobId:         fmt.Sprintf("%s-%s", req.PipelineID, req.JobName),
		JobName:       req.JobName,
		StepName:      req.StepName,
		StepIndex:     int32(req.StepIndex),
		Uses:          req.Step.Uses,
		Action:        req.Step.Action,
		Args:          argsStruct,
		Env:           env,
		Workspace:     req.Workspace,
		Timeout:       fmt.Sprintf("%ds", timeout),
		AgentSelector: labelSelector,
		Plugins:       plugins,
	}

	return stepRun, nil
}

// convertAgentSelectorToStepRunSelector converts agent AgentSelector to steprun AgentSelector
func (am *AgentManager) convertAgentSelectorToStepRunSelector(agentSelector *agentv1.AgentSelector) *steprunv1.AgentSelector {
	if agentSelector == nil {
		return nil
	}

	stepRunSelector := &steprunv1.AgentSelector{
		MatchLabels: agentSelector.MatchLabels,
	}

	if len(agentSelector.MatchExpressions) > 0 {
		expressions := make([]*steprunv1.LabelExpression, 0, len(agentSelector.MatchExpressions))
		for _, expr := range agentSelector.MatchExpressions {
			expressions = append(expressions, &steprunv1.LabelExpression{
				Key:      expr.Key,
				Operator: expr.Operator,
				Values:   expr.Values,
			})
		}
		stepRunSelector.MatchExpressions = expressions
	}

	return stepRunSelector
}

// convertAgentPluginsToStepRunPlugins converts agent PluginInfo list to steprun PluginInfo list
func (am *AgentManager) convertAgentPluginsToStepRunPlugins(agentPlugins []*agentv1.PluginInfo) []*steprunv1.PluginInfo {
	if len(agentPlugins) == 0 {
		return nil
	}

	stepRunPlugins := make([]*steprunv1.PluginInfo, 0, len(agentPlugins))
	for _, plugin := range agentPlugins {
		stepRunPlugins = append(stepRunPlugins, &steprunv1.PluginInfo{
			PluginId: plugin.PluginId,
			Name:     plugin.Name,
			Version:  plugin.Version,
			Checksum: plugin.Checksum,
			Size:     plugin.Size,
			// DownloadUrl and Location would be populated from plugin registry
		})
	}

	return stepRunPlugins
}

// buildCommands builds command list from step action and params
// This method calls plugin to resolve step into concrete commands before sending to agent
func (am *AgentManager) buildCommands(step *spec.Step, ctx *ExecutionContext) ([]string, error) {
	if ctx == nil || ctx.PluginManager == nil {
		return nil, fmt.Errorf("execution context or plugin manager is not available")
	}

	// Get plugin instance
	pluginInstance, err := ctx.PluginManager.GetPlugin(step.Uses)
	if err != nil {
		return nil, fmt.Errorf("plugin not found: %s: %w", step.Uses, err)
	}

	// Determine action (default to "Execute" if not specified)
	action := step.Action
	if action == "" {
		action = "Execute"
	}

	// Resolve params with variable substitution
	resolvedParams := ctx.ResolveVariables(step.Args)

	// Prepare params JSON
	paramsJSON, err := json.Marshal(resolvedParams)
	if err != nil {
		return nil, fmt.Errorf("marshal params: %w", err)
	}

	// Prepare opts JSON with dry-run flag to get commands instead of executing
	opts := map[string]any{
		"workspace":       ctx.StepWorkspace("", step.Name), // Will be set properly when step run is created
		"dry_run":         true,                             // Request plugin to return commands instead of executing
		"build_for_agent": true,                             // Indicate this is for agent execution
	}
	optsJSON, err := json.Marshal(opts)
	if err != nil {
		return nil, fmt.Errorf("marshal opts: %w", err)
	}

	// Try to call plugin's BuildCommands action first (if supported)
	// This allows plugins to return command list without executing
	commandsJSON, err := pluginInstance.Execute("BuildCommands", paramsJSON, optsJSON)
	if err == nil {
		// Plugin supports BuildCommands, parse the returned commands
		var commands []string
		if err := json.Unmarshal(commandsJSON, &commands); err != nil {
			return nil, fmt.Errorf("unmarshal commands from plugin: %w", err)
		}
		if len(commands) > 0 {
			return commands, nil
		}
	}

	// Fallback: For plugins that don't support BuildCommands, build commands based on plugin type
	// This is a compatibility layer for existing plugins
	pluginName := pluginInstance.Name()
	switch pluginName {
	case "shell":
		return am.buildShellCommands(step, resolvedParams, ctx)
	default:
		// For other plugins, try to build command based on common patterns
		return am.buildGenericPluginCommand(step, action), nil
	}
}

// buildShellCommands builds shell commands from step params
func (am *AgentManager) buildShellCommands(step *spec.Step, params map[string]any, _ *ExecutionContext) ([]string, error) {
	commands := []string{}

	// Determine shell action
	action := step.Action
	if action == "" {
		action = "command" // Default to command for shell plugin
	}

	// Build command based on action type
	switch action {
	case "script":
		// For script action, create a script file and execute it
		if script, ok := params["script"].(string); ok && script != "" {
			// Create a command that writes script to file and executes it
			scriptFile := "/tmp/script.sh"
			commands = append(commands, fmt.Sprintf("cat > %s << 'SCRIPT_EOF'\n%s\nSCRIPT_EOF", scriptFile, script))
			commands = append(commands, fmt.Sprintf("chmod +x %s", scriptFile))

			// Add script arguments if provided
			scriptArgs := ""
			if args, ok := params["args"].([]any); ok && len(args) > 0 {
				argStrs := make([]string, len(args))
				for i, arg := range args {
					argStrs[i] = fmt.Sprintf("%v", arg)
				}
				scriptArgs = " " + strings.Join(argStrs, " ")
			}

			commands = append(commands, fmt.Sprintf("sh %s%s", scriptFile, scriptArgs))
			commands = append(commands, fmt.Sprintf("rm -f %s", scriptFile))
		} else {
			return nil, fmt.Errorf("script parameter is required for shell script action")
		}

	case "command":
		// For command action, execute directly
		if command, ok := params["command"].(string); ok && command != "" {
			// Add command arguments if provided
			if args, ok := params["args"].([]any); ok && len(args) > 0 {
				argStrs := make([]string, len(args))
				for i, arg := range args {
					argStrs[i] = fmt.Sprintf("%v", arg)
				}
				commands = append(commands, fmt.Sprintf("%s %s", command, strings.Join(argStrs, " ")))
			} else {
				commands = append(commands, command)
			}
		} else {
			return nil, fmt.Errorf("command parameter is required for shell command action")
		}

	default:
		return nil, fmt.Errorf("unsupported shell action: %s", action)
	}

	return commands, nil
}

// buildGenericPluginCommand builds a generic plugin command as fallback
func (am *AgentManager) buildGenericPluginCommand(step *spec.Step, action string) []string {
	// This is a fallback for plugins that don't support BuildCommands
	// In this case, we still send plugin command to agent
	// Agent will need to have plugin runtime to execute it
	// TODO: This should be improved to fully resolve commands in main program
	return []string{fmt.Sprintf("plugin execute --plugin %s --action %s", step.Uses, action)}
}

// buildLabelSelector builds agent label selector from step selector
func (am *AgentManager) buildLabelSelector(selector *spec.AgentSelector) *agentv1.AgentSelector {
	if selector == nil {
		return nil
	}

	agentSelector := &agentv1.AgentSelector{
		MatchLabels: selector.MatchLabels,
	}

	// Convert match expressions (from string operator to agent LabelExpression)
	if len(selector.MatchExpressions) > 0 {
		expressions := make([]*agentv1.LabelExpression, 0, len(selector.MatchExpressions))
		for _, expr := range selector.MatchExpressions {
			expressions = append(expressions, &agentv1.LabelExpression{
				Key:      expr.Key,
				Operator: expr.Operator,
				Values:   expr.Values,
			})
		}
		agentSelector.MatchExpressions = expressions
	}

	return agentSelector
}

// createStepRun creates a step run in the step run service
func (am *AgentManager) createStepRun(ctx context.Context, req *StepExecutionRequest, agentStepRun *agentv1.StepRun) (string, error) {
	if am.stepRunClient == nil {
		return "", fmt.Errorf("step run client is not initialized")
	}

	// Convert agent step run to step run service request
	createReq := &steprunv1.CreateStepRunRequest{
		PipelineId:      agentStepRun.PipelineId,
		JobId:           agentStepRun.JobId,
		JobName:         agentStepRun.JobName,
		StepName:        agentStepRun.StepName,
		StepIndex:       agentStepRun.StepIndex,
		Uses:            agentStepRun.Uses,
		Action:          agentStepRun.Action,
		Args:            agentStepRun.Args,
		Env:             agentStepRun.Env,
		Workspace:       agentStepRun.Workspace,
		Timeout:         agentStepRun.Timeout,
		ContinueOnError: req.Step.ContinueOnError,
		When:            req.Step.When,
		AgentSelector:   am.convertAgentSelectorToStepRunSelector(agentStepRun.AgentSelector),
		Plugins:         am.convertAgentPluginsToStepRunPlugins(agentStepRun.Plugins),
	}

	// Create step run
	resp, err := am.stepRunClient.CreateStepRun(ctx, createReq)
	if err != nil {
		return "", fmt.Errorf("create step run via step run service: %w", err)
	}

	if !resp.Success {
		return "", fmt.Errorf("create step run failed: %s", resp.Message)
	}

	return resp.StepRunId, nil
}

// WaitForStepRunCompletion waits for a step run to complete on an agent
// It polls the step run status from step run service and waits for completion
func (am *AgentManager) WaitForStepRunCompletion(ctx context.Context, stepRunID string, timeout time.Duration) (*StepExecutionResult, error) {
	if am.stepRunClient == nil {
		return nil, fmt.Errorf("step run client is not initialized")
	}

	// Create context with timeout
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Poll interval
	pollInterval := 2 * time.Second
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	result := &StepExecutionResult{
		StartTime: time.Now(),
		Success:   false,
		Metrics:   make(map[string]string),
	}

	for {
		select {
		case <-ctx.Done():
			// Timeout or context cancelled
			if ctx.Err() == context.DeadlineExceeded {
				// Mark step run as timeout
				if err := am.cancelStepRun(ctx, stepRunID, "step run execution timeout"); err != nil {
					if am.logger.Log != nil {
						am.logger.Log.Warnw("failed to cancel timeout step run", "stepRun", stepRunID, "error", err)
					}
				}
				return nil, fmt.Errorf("step run %s execution timeout after %v", stepRunID, timeout)
			}
			return nil, ctx.Err()

		case <-ticker.C:
			// Poll step run status
			stepRun, err := am.getStepRunStatus(ctx, stepRunID)
			if err != nil {
				if am.logger.Log != nil {
					am.logger.Log.Warnw("failed to get step run status", "stepRun", stepRunID, "error", err)
				}
				continue
			}

			if stepRun == nil {
				continue
			}

			// Update result based on step run status
			switch stepRun.Status {
			case steprunv1.StepRunStatus_STEP_RUN_STATUS_SUCCESS:
				// Step run completed successfully
				result.Success = true
				result.ExitCode = stepRun.ExitCode
				if stepRun.FinishedAt > 0 {
					result.EndTime = time.Unix(stepRun.FinishedAt/1000, (stepRun.FinishedAt%1000)*1000000)
				} else {
					result.EndTime = time.Now()
				}
				if stepRun.StartedAt > 0 {
					result.StartTime = time.Unix(stepRun.StartedAt/1000, (stepRun.StartedAt%1000)*1000000)
				}
				if am.logger.Log != nil {
					am.logger.Log.Infow("step run completed successfully", "stepRun", stepRunID)
				}
				return result, nil

			case steprunv1.StepRunStatus_STEP_RUN_STATUS_FAILED:
				// Step run failed
				result.Success = false
				result.ExitCode = stepRun.ExitCode
				result.Error = stepRun.ErrorMessage
				if stepRun.FinishedAt > 0 {
					result.EndTime = time.Unix(stepRun.FinishedAt/1000, (stepRun.FinishedAt%1000)*1000000)
				} else {
					result.EndTime = time.Now()
				}
				if stepRun.StartedAt > 0 {
					result.StartTime = time.Unix(stepRun.StartedAt/1000, (stepRun.StartedAt%1000)*1000000)
				}
				if am.logger.Log != nil {
					am.logger.Log.Errorw("step run failed", "stepRun", stepRunID, "error", stepRun.ErrorMessage)
				}
				return result, fmt.Errorf("step run execution failed: %s", stepRun.ErrorMessage)

			case steprunv1.StepRunStatus_STEP_RUN_STATUS_CANCELLED:
				// Step run cancelled
				result.Success = false
				result.ExitCode = -1
				result.Error = "step run cancelled"
				result.EndTime = time.Now()
				if am.logger.Log != nil {
					am.logger.Log.Warnw("step run was cancelled", "stepRun", stepRunID)
				}
				return result, fmt.Errorf("step run was cancelled")

			case steprunv1.StepRunStatus_STEP_RUN_STATUS_TIMEOUT:
				// Step run timeout
				result.Success = false
				result.ExitCode = -1
				result.Error = "step run execution timeout"
				result.EndTime = time.Now()
				if am.logger.Log != nil {
					am.logger.Log.Warnw("step run timed out", "stepRun", stepRunID)
				}
				return result, fmt.Errorf("step run execution timeout")

			case steprunv1.StepRunStatus_STEP_RUN_STATUS_RUNNING:
				// Step run is running, continue polling
				if stepRun.StartedAt > 0 && result.StartTime.IsZero() {
					result.StartTime = time.Unix(stepRun.StartedAt/1000, (stepRun.StartedAt%1000)*1000000)
				}
				continue

			case steprunv1.StepRunStatus_STEP_RUN_STATUS_PENDING, steprunv1.StepRunStatus_STEP_RUN_STATUS_QUEUED:
				// Step run is pending or queued, continue polling
				continue

			default:
				// Unknown status, continue polling
				continue
			}
		}
	}
}

// getStepRunStatus gets step run status from step run service
func (am *AgentManager) getStepRunStatus(ctx context.Context, stepRunID string) (*steprunv1.StepRunDetail, error) {
	req := &steprunv1.GetStepRunRequest{
		StepRunId: stepRunID,
	}

	resp, err := am.stepRunClient.GetStepRun(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("get step run status: %w", err)
	}

	if !resp.Success {
		return nil, fmt.Errorf("get step run failed: %s", resp.Message)
	}

	return resp.StepRun, nil
}

// cancelStepRun cancels a step run
func (am *AgentManager) cancelStepRun(ctx context.Context, stepRunID, reason string) error {
	if am.stepRunClient == nil {
		return fmt.Errorf("step run client is not initialized")
	}

	req := &steprunv1.CancelStepRunRequest{
		StepRunId: stepRunID,
		Reason:    reason,
	}

	resp, err := am.stepRunClient.CancelStepRun(ctx, req)
	if err != nil {
		return fmt.Errorf("cancel step run: %w", err)
	}

	if !resp.Success {
		return fmt.Errorf("cancel step run failed: %s", resp.Message)
	}

	return nil
}

// CancelStepRun cancels a running step run on an agent
func (am *AgentManager) CancelStepRun(ctx context.Context, agentID, stepRunID, reason string) error {
	if am.agentClient == nil {
		return fmt.Errorf("agent client is not initialized")
	}

	req := &agentv1.CancelStepRunRequest{
		AgentId:   agentID,
		StepRunId: stepRunID,
		Reason:    reason,
	}

	_, err := am.agentClient.CancelStepRun(ctx, req)
	if err != nil {
		return fmt.Errorf("cancel step run: %w", err)
	}

	if am.logger.Log != nil {
		am.logger.Log.Infow("cancelled step run on agent", "stepRun", stepRunID, "agent", agentID, "reason", reason)
	}

	return nil
}

// GetAgentStatus gets the status of an agent
// Agent status is updated from heartbeat reports, so we retrieve from cache
func (am *AgentManager) GetAgentStatus(ctx context.Context, agentID string) (*AgentStatus, error) {
	if agentID == "" {
		return nil, fmt.Errorf("agent ID is required")
	}

	am.statusCacheMu.RLock()
	status, exists := am.agentStatusCache[agentID]
	am.statusCacheMu.RUnlock()

	if !exists {
		// Agent not found in cache, return offline status
		return &AgentStatus{
			AgentID:               agentID,
			Status:                "offline",
			RunningStepRunsCount:  0,
			MaxConcurrentStepRuns: 0,
			Metrics:               make(map[string]string),
			Labels:                make(map[string]string),
			LastHeartbeat:         time.Time{},
		}, nil
	}

	// Check if heartbeat is stale (more than 5 minutes old)
	if time.Since(status.LastHeartbeat) > 5*time.Minute {
		// Mark as offline if heartbeat is stale
		status.Status = "offline"
	}

	return status, nil
}

// UpdateAgentStatusFromHeartbeat updates agent status from heartbeat report
// This should be called by the agent service when processing heartbeat requests
func (am *AgentManager) UpdateAgentStatusFromHeartbeat(req *agentv1.HeartbeatRequest) {
	if req == nil || req.AgentId == "" {
		return
	}

	am.statusCacheMu.Lock()
	defer am.statusCacheMu.Unlock()

	status := &AgentStatus{
		AgentID:              req.AgentId,
		Status:               am.convertAgentStatusToString(req.Status),
		RunningStepRunsCount: req.RunningStepRunsCount,
		LastHeartbeat:        time.Now(),
	}

	am.agentStatusCache[req.AgentId] = status

	if am.logger.Log != nil {
		am.logger.Log.Debugw("updated agent status", "agent", req.AgentId, "status", status.Status, "running_step_runs", req.RunningStepRunsCount)
	}
}

// convertAgentStatusToString converts agent status enum to string
func (am *AgentManager) convertAgentStatusToString(status agentv1.AgentStatus) string {
	switch status {
	case agentv1.AgentStatus_AGENT_STATUS_ONLINE:
		return "online"
	case agentv1.AgentStatus_AGENT_STATUS_OFFLINE:
		return "offline"
	case agentv1.AgentStatus_AGENT_STATUS_BUSY:
		return "busy"
	case agentv1.AgentStatus_AGENT_STATUS_IDLE:
		return "idle"
	default:
		return "unknown"
	}
}

// ListAgents lists all agents with their status
func (am *AgentManager) ListAgents() []*AgentStatus {
	am.statusCacheMu.RLock()
	defer am.statusCacheMu.RUnlock()

	agents := make([]*AgentStatus, 0, len(am.agentStatusCache))
	for _, status := range am.agentStatusCache {
		// Check if heartbeat is stale
		if time.Since(status.LastHeartbeat) > 5*time.Minute {
			status.Status = "offline"
		}
		agents = append(agents, status)
	}

	return agents
}

// RemoveAgentStatus removes agent status from cache (when agent unregisters)
func (am *AgentManager) RemoveAgentStatus(agentID string) {
	am.statusCacheMu.Lock()
	defer am.statusCacheMu.Unlock()

	delete(am.agentStatusCache, agentID)

	if am.logger.Log != nil {
		am.logger.Log.Debugw("removed agent from status cache", "agent", agentID)
	}
}

// AgentStatus represents agent status information
type AgentStatus struct {
	AgentID               string
	Status                string
	RunningStepRunsCount  int32
	MaxConcurrentStepRuns int32
	Metrics               map[string]string
	Labels                map[string]string // Agent labels for matching
	LastHeartbeat         time.Time
}
