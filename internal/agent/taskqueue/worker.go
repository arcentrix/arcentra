// Copyright 2025 Arcentra Authors.
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

package taskqueue

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	steprunv1 "github.com/arcentrix/arcentra/api/steprun/v1"
	"github.com/arcentrix/arcentra/internal/agent/config"
	"github.com/arcentrix/arcentra/internal/pkg/executor"
	"github.com/arcentrix/arcentra/internal/pkg/grpc"
	"github.com/arcentrix/arcentra/internal/pkg/storage"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/nova"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
)

var (
	runningStepRunCancel sync.Map
	runningJobRunCancel  sync.Map
	storageInstance      storage.IStorage
)

// SetStorage sets the object-storage client used by job workers for artifact
// upload/download. Called after the agent registers and receives StorageConfig.
func SetStorage(st storage.IStorage) {
	storageInstance = st
}

// StartWorker starts the task queue worker. When execManager is non-nil, step run tasks
// are executed via executor.Manager (events go through Outbox when SetEventPublisher is used);
// otherwise the legacy executeStepRun (shell only) is used.
func StartWorker(
	_ context.Context,
	agentConf *config.AgentConfig,
	grpcClient *grpc.ClientWrapper,
	execManager *executor.Manager,
) (nova.TaskQueue, error) {
	if agentConf == nil {
		return nil, nil
	}
	cfg := agentConf.MessageQueue.Kafka
	if cfg.BootstrapServers == "" {
		return nil, nil
	}
	queueCfg := agentConf.TaskQueue
	delaySlotDuration := time.Duration(queueCfg.DelaySlotDuration) * time.Second
	options := []nova.QueueOption{
		nova.WithKafka(cfg.BootstrapServers,
			nova.WithKafkaAuth(cfg.SecurityProtocol, cfg.Sasl.Mechanism, cfg.Sasl.Username, cfg.Sasl.Password),
			nova.WithKafkaSSL(cfg.Ssl.CaFile, cfg.Ssl.CertFile, cfg.Ssl.KeyFile, cfg.Ssl.Password),
			nova.WithKafkaClientProgramName("arcentra-agent"),
			nova.WithKafkaAutoCommit(queueCfg.AutoCommit),
			nova.WithKafkaSessionTimeout(queueCfg.SessionTimeout),
			nova.WithKafkaMaxPollInterval(queueCfg.MaxPollInterval),
			nova.WithKafkaDelaySlots(queueCfg.DelaySlotCount, delaySlotDuration),
		),
	}
	if opt := withMessageFormat(queueCfg.MessageFormat); opt != nil {
		options = append(options, opt)
	}
	if opt := withMessageCodec(queueCfg.MessageCodec); opt != nil {
		options = append(options, opt)
	}
	queue, err := nova.NewTaskQueue(options...)
	if err != nil {
		return nil, fmt.Errorf("create task queue: %w", err)
	}

	handler := nova.HandlerFunc(func(ctx context.Context, task *nova.Task) error {
		if task == nil {
			return nil
		}
		switch task.Type {
		case taskqueue.TaskTypeJobRun:
			var payload taskqueue.JobRunTaskPayload
			if err := sonic.Unmarshal(task.Payload, &payload); err != nil {
				return fmt.Errorf("unmarshal job run payload: %w", err)
			}
			log.Infow("received job run task",
				"jobRunId", payload.JobRunID,
				"jobName", payload.JobName,
				"steps", len(payload.Steps),
			)
			return executeJobRun(ctx, agentConf, grpcClient, execManager, &payload)
		case taskqueue.TaskTypeStepRun:
			var payload taskqueue.StepRunTaskPayload
			if err := sonic.Unmarshal(task.Payload, &payload); err != nil {
				return fmt.Errorf("unmarshal step run payload: %w", err)
			}
			log.Infow("received step run task",
				"stepRunId", payload.StepRunID,
				"jobName", payload.JobName,
				"stepName", payload.StepName,
			)
			if execManager != nil {
				return executeStepRunViaExecutor(ctx, agentConf, grpcClient, execManager, &payload)
			}
			return executeStepRun(ctx, agentConf, grpcClient, &payload)
		default:
			log.Debugw("unknown task type", "type", task.Type)
			return nil
		}
	})

	if err := queue.Start(handler); err != nil {
		return nil, fmt.Errorf("start task queue: %w", err)
	}

	return queue, nil
}

func withMessageFormat(value string) nova.QueueOption {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return nil
	}
	return nova.WithMessageFormat(nova.MessageFormat(value))
}

func withMessageCodec(value string) nova.QueueOption {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return nil
	}
	codec, err := nova.NewMessageCodec(nova.MessageFormat(value))
	if err != nil {
		return nil
	}
	return nova.WithMessageCodec(codec)
}

func executeStepRunViaExecutor(
	ctx context.Context,
	agentConf *config.AgentConfig,
	grpcClient *grpc.ClientWrapper,
	execManager *executor.Manager,
	payload *taskqueue.StepRunTaskPayload,
) error {
	if payload == nil {
		return nil
	}
	stepCtx, cancel := context.WithCancel(ctx)
	runningStepRunCancel.Store(payload.StepRunID, cancel)
	defer func() {
		runningStepRunCancel.Delete(payload.StepRunID)
		cancel()
	}()

	start := time.Now()
	_ = reportStepRunStatus(grpcClient, agentConf, payload.StepRunID, steprunv1.StepRunStatus_STEP_RUN_STATUS_RUNNING, 0, "", start, 0, nil)

	req := PayloadToExecutionRequest(payload, agentConf.Agent.JobTimeout)
	result, execErr := execManager.Execute(stepCtx, req)
	end := time.Now().Unix()
	metrics := map[string]string{"executor": "agent-shell"}
	if result != nil && result.Metadata != nil {
		if v, ok := result.Metadata["outputBytes"]; ok {
			if s, ok := v.(string); ok {
				metrics["outputBytes"] = s
			}
		}
	}
	if result != nil && len(result.Output) > 0 {
		metrics["outputBytes"] = fmt.Sprintf("%d", len(result.Output))
	}
	status, exitCode, errMsg := resultToStepRunStatus(stepCtx, result, execErr)
	_ = reportStepRunStatus(grpcClient, agentConf, payload.StepRunID, status, exitCode, errMsg, start, end, metrics)
	return execErr
}

func resultToStepRunStatus(ctx context.Context, result *executor.ExecutionResult, execErr error) (steprunv1.StepRunStatus, int32, string) {
	if ctx.Err() == context.DeadlineExceeded {
		return steprunv1.StepRunStatus_STEP_RUN_STATUS_TIMEOUT, 1, "timeout"
	}
	if ctx.Err() == context.Canceled {
		return steprunv1.StepRunStatus_STEP_RUN_STATUS_CANCELLED, 1, "cancelled"
	}
	if result == nil {
		errMsg := ""
		if execErr != nil {
			errMsg = execErr.Error()
		}
		return steprunv1.StepRunStatus_STEP_RUN_STATUS_FAILED, 1, errMsg
	}
	if result.Success {
		return steprunv1.StepRunStatus_STEP_RUN_STATUS_SUCCESS, result.ExitCode, ""
	}
	return steprunv1.StepRunStatus_STEP_RUN_STATUS_FAILED, result.ExitCode, result.Error
}

func CancelStepRun(stepRunID string) bool {
	value, ok := runningStepRunCancel.Load(stepRunID)
	if !ok {
		return false
	}
	cancel, ok := value.(context.CancelFunc)
	if !ok {
		return false
	}
	cancel()
	return true
}

// CancelJobRun cancels a running job by its jobRunID.
func CancelJobRun(jobRunID string) bool {
	value, ok := runningJobRunCancel.Load(jobRunID)
	if !ok {
		return false
	}
	cancel, ok := value.(context.CancelFunc)
	if !ok {
		return false
	}
	cancel()
	return true
}

// executeJobRun handles a complete job: executes all steps sequentially,
// reports per-step status, and reports job-level terminal status.
func executeJobRun(
	ctx context.Context,
	agentConf *config.AgentConfig,
	grpcClient *grpc.ClientWrapper,
	execManager *executor.Manager,
	payload *taskqueue.JobRunTaskPayload,
) error {
	if payload == nil {
		return nil
	}

	jobCtx, cancel := context.WithCancel(ctx)
	runningJobRunCancel.Store(payload.JobRunID, cancel)
	defer func() {
		runningJobRunCancel.Delete(payload.JobRunID)
		cancel()
	}()

	if payload.Timeout != "" {
		if d, err := time.ParseDuration(payload.Timeout); err == nil && d > 0 {
			var timeoutCancel context.CancelFunc
			jobCtx, timeoutCancel = context.WithTimeout(jobCtx, d)
			defer timeoutCancel()
		}
	}

	start := time.Now()
	reportJobRunStatus(grpcClient, agentConf, payload.JobRunID, agentv1.AgentStatus_AGENT_STATUS_BUSY, int32(steprunv1.StepRunStatus_STEP_RUN_STATUS_RUNNING), "", start, 0)

	workspace := payload.Workspace
	if workspace == "" {
		workspace = "."
	}

	if err := handleSourceClone(jobCtx, payload.Source, workspace); err != nil {
		end := time.Now().Unix()
		reportJobRunStatus(grpcClient, agentConf, payload.JobRunID, agentv1.AgentStatus_AGENT_STATUS_IDLE,
			int32(steprunv1.StepRunStatus_STEP_RUN_STATUS_FAILED), err.Error(), start, end)
		return fmt.Errorf("source clone failed: %w", err)
	}

	if err := handleArtifactDownload(jobCtx, payload.ArtifactURIs, workspace, storageInstance); err != nil {
		log.Warnw("artifact download failed (non-fatal)", "jobRunId", payload.JobRunID, "error", err)
	}

	var jobErr error
	for _, step := range payload.Steps {
		if jobCtx.Err() != nil {
			break
		}
		stepStart := time.Now()
		_ = reportStepRunStatus(grpcClient, agentConf, step.StepRunID,
			steprunv1.StepRunStatus_STEP_RUN_STATUS_RUNNING, 0, "", stepStart, 0, nil)

		stepPayload := &taskqueue.StepRunTaskPayload{
			PipelineID:    payload.PipelineID,
			PipelineRunID: payload.PipelineRunID,
			JobID:         payload.JobRunID,
			JobName:       payload.JobName,
			StepName:      step.Name,
			StepIndex:     step.StepIndex,
			StepRunID:     step.StepRunID,
			Uses:          step.Uses,
			Action:        step.Action,
			Args:          step.Args,
			Env:           mergeEnv(payload.Env, step.Env),
			Workspace:     payload.Workspace,
			Timeout:       step.Timeout,
		}

		var stepErr error
		if execManager != nil {
			stepErr = executeStepRunViaExecutor(jobCtx, agentConf, grpcClient, execManager, stepPayload)
		} else {
			stepErr = executeStepRun(jobCtx, agentConf, grpcClient, stepPayload)
		}

		if stepErr != nil && !step.ContinueOnError {
			jobErr = fmt.Errorf("step %s failed: %w", step.Name, stepErr)
			break
		}
	}

	end := time.Now().Unix()
	status := int32(steprunv1.StepRunStatus_STEP_RUN_STATUS_SUCCESS)
	errMsg := ""
	if jobErr != nil {
		status = int32(steprunv1.StepRunStatus_STEP_RUN_STATUS_FAILED)
		errMsg = jobErr.Error()
	}
	if jobCtx.Err() == context.DeadlineExceeded {
		status = int32(steprunv1.StepRunStatus_STEP_RUN_STATUS_TIMEOUT)
		errMsg = "job timeout"
	}
	if jobCtx.Err() == context.Canceled {
		status = int32(steprunv1.StepRunStatus_STEP_RUN_STATUS_CANCELLED)
		errMsg = "job cancelled"
	}

	reportJobRunStatus(grpcClient, agentConf, payload.JobRunID, agentv1.AgentStatus_AGENT_STATUS_IDLE, status, errMsg, start, end)
	return jobErr
}

// reportJobRunStatus reports job run status to the control plane using the
// dedicated ReportJobRunStatus RPC.
func reportJobRunStatus(
	grpcClient *grpc.ClientWrapper,
	agentConf *config.AgentConfig,
	jobRunID string,
	_ agentv1.AgentStatus,
	status int32,
	errMsg string,
	start time.Time,
	endUnix int64,
) {
	if grpcClient == nil || grpcClient.AgentClient == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	agentID := ""
	if agentConf != nil {
		agentID = agentConf.Agent.ID
	}
	req := &agentv1.ReportJobRunStatusRequest{
		AgentId:      agentID,
		JobRunId:     jobRunID,
		Status:       status,
		ErrorMessage: errMsg,
		StartTime:    start.Unix(),
		EndTime:      endUnix,
	}
	if _, err := grpcClient.AgentClient.ReportJobRunStatus(ctx, req); err != nil {
		log.Warnw("report job run status failed", "jobRunId", jobRunID, "error", err)
	}
}

// mergeEnv merges job-level and step-level environment variables.
// Step env takes precedence over job env.
func mergeEnv(jobEnv, stepEnv map[string]string) map[string]string {
	if len(jobEnv) == 0 && len(stepEnv) == 0 {
		return nil
	}
	merged := make(map[string]string, len(jobEnv)+len(stepEnv))
	for k, v := range jobEnv {
		merged[k] = v
	}
	for k, v := range stepEnv {
		merged[k] = v
	}
	return merged
}

func executeStepRun(
	ctx context.Context,
	agentConf *config.AgentConfig,
	grpcClient *grpc.ClientWrapper,
	payload *taskqueue.StepRunTaskPayload,
) error {
	if payload == nil {
		return nil
	}
	stepCtx, cancel := context.WithCancel(ctx)
	runningStepRunCancel.Store(payload.StepRunID, cancel)
	defer func() {
		runningStepRunCancel.Delete(payload.StepRunID)
		cancel()
	}()

	start := time.Now()
	_ = reportStepRunStatus(grpcClient, agentConf, payload.StepRunID, steprunv1.StepRunStatus_STEP_RUN_STATUS_RUNNING, 0, "", start, 0, nil)

	timeout := parseTimeout(payload.Timeout, agentConf.Agent.JobTimeout)
	runCtx := stepCtx
	var timeoutCancel context.CancelFunc
	if timeout > 0 {
		runCtx, timeoutCancel = context.WithTimeout(stepCtx, timeout)
		defer timeoutCancel()
	}

	cmdText := buildCommandFromPayload(payload)
	if strings.TrimSpace(cmdText) == "" {
		err := fmt.Errorf("empty command for step run %s", payload.StepRunID)
		_ = reportStepRunStatus(
			grpcClient,
			agentConf,
			payload.StepRunID,
			steprunv1.StepRunStatus_STEP_RUN_STATUS_FAILED,
			1,
			err.Error(),
			start,
			time.Now().Unix(),
			map[string]string{"executor": "agent-shell"},
		)
		return err
	}

	cmd := exec.CommandContext(runCtx, "sh", "-lc", cmdText)
	if strings.TrimSpace(payload.Workspace) != "" {
		cmd.Dir = payload.Workspace
	}
	for k, v := range payload.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	output, err := cmd.CombinedOutput()
	end := time.Now().Unix()
	metrics := map[string]string{
		"executor":    "agent-shell",
		"outputBytes": fmt.Sprintf("%d", len(output)),
	}
	if err != nil {
		status := steprunv1.StepRunStatus_STEP_RUN_STATUS_FAILED
		if runCtx.Err() == context.DeadlineExceeded {
			status = steprunv1.StepRunStatus_STEP_RUN_STATUS_TIMEOUT
		}
		if runCtx.Err() == context.Canceled {
			status = steprunv1.StepRunStatus_STEP_RUN_STATUS_CANCELLED
		}
		exitCode := int32(1)
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = int32(exitErr.ExitCode())
		}
		errMsg := strings.TrimSpace(string(output))
		if errMsg == "" {
			errMsg = err.Error()
		}
		_ = reportStepRunStatus(grpcClient, agentConf, payload.StepRunID, status, exitCode, errMsg, start, end, metrics)
		return err
	}

	_ = reportStepRunStatus(
		grpcClient,
		agentConf,
		payload.StepRunID,
		steprunv1.StepRunStatus_STEP_RUN_STATUS_SUCCESS,
		0,
		"",
		start,
		end,
		metrics,
	)
	return nil
}

func reportStepRunStatus(
	grpcClient *grpc.ClientWrapper,
	agentConf *config.AgentConfig,
	stepRunID string,
	status steprunv1.StepRunStatus,
	exitCode int32,
	errMsg string,
	start time.Time,
	endUnix int64,
	metrics map[string]string,
) error {
	if grpcClient == nil || grpcClient.AgentClient == nil || agentConf == nil {
		return nil
	}
	ctx, cancel := grpcClient.WithTimeoutAndAuth(context.Background())
	defer cancel()
	req := &agentv1.ReportStepRunStatusRequest{
		AgentId:      agentConf.Agent.ID,
		StepRunId:    stepRunID,
		Status:       status,
		ExitCode:     exitCode,
		ErrorMessage: errMsg,
		StartTime:    start.Unix(),
		EndTime:      endUnix,
		Metrics:      metrics,
	}
	_, err := grpcClient.AgentClient.ReportStepRunStatus(ctx, req)
	return err
}

func buildCommandFromPayload(payload *taskqueue.StepRunTaskPayload) string {
	if payload == nil {
		return ""
	}
	for _, key := range []string{"run", "script", "command"} {
		if value, ok := payload.Args[key]; ok {
			if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
				return text
			}
		}
	}
	if value, ok := payload.Args["commands"]; ok {
		if list, ok := value.([]any); ok {
			parts := make([]string, 0, len(list))
			for i := range list {
				if text, ok := list[i].(string); ok && strings.TrimSpace(text) != "" {
					parts = append(parts, text)
				}
			}
			return strings.Join(parts, "\n")
		}
	}
	return ""
}

func parseTimeout(raw string, defaultSeconds int) time.Duration {
	if strings.TrimSpace(raw) != "" {
		if timeout, err := time.ParseDuration(raw); err == nil {
			return timeout
		}
	}
	if defaultSeconds > 0 {
		return time.Duration(defaultSeconds) * time.Second
	}
	return 0
}

// handleSourceClone clones the source repository into the workspace directory
// when the job payload contains a SourcePayload with type "git".
func handleSourceClone(ctx context.Context, src *taskqueue.SourcePayload, workspace string) error {
	if src == nil || src.Type == "" {
		return nil
	}
	if !strings.EqualFold(src.Type, "git") || src.Repo == "" {
		return nil
	}

	_ = os.MkdirAll(workspace, 0o755)

	args := []string{"clone", "--depth", "1"}
	if src.Branch != "" {
		args = append(args, "--branch", src.Branch)
	}
	args = append(args, src.Repo, workspace)

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = workspace
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git clone failed: %s: %w", string(output), err)
	}
	log.Infow("source clone complete", "repo", src.Repo, "branch", src.Branch, "workspace", workspace)
	return nil
}

// handleArtifactDownload downloads upstream job artifacts from object storage
// into the workspace. Each entry in uris maps "jobName/artifactName" to an
// object key (or URI) in the configured bucket.
func handleArtifactDownload(ctx context.Context, uris map[string]string, workspace string, st storage.IStorage) error {
	if len(uris) == 0 || st == nil {
		return nil
	}

	artifactsDir := filepath.Join(workspace, ".artifacts")
	_ = os.MkdirAll(artifactsDir, 0o755)

	for key, objectName := range uris {
		if objectName == "" {
			continue
		}
		data, err := st.Download(ctx, objectName)
		if err != nil {
			log.Warnw("artifact download failed", "key", key, "object", objectName, "error", err)
			continue
		}
		localPath := filepath.Join(artifactsDir, strings.ReplaceAll(key, "/", "_"))
		if err := os.WriteFile(localPath, data, 0o644); err != nil {
			log.Warnw("artifact write failed", "key", key, "localPath", localPath, "error", err)
			continue
		}
		log.Infow("artifact downloaded", "key", key, "localPath", localPath)
	}
	return nil
}
