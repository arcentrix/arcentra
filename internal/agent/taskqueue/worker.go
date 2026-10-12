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
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	steprunv1 "github.com/arcentrix/arcentra/api/steprun/v1"
	"github.com/arcentrix/arcentra/internal/agent/config"
	"github.com/arcentrix/arcentra/internal/shared/executor"
	"github.com/arcentrix/arcentra/internal/shared/grpc"
	"github.com/arcentrix/arcentra/internal/shared/storage"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/nova"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
	"github.com/expr-lang/expr"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

var (
	runningStepRunCancel sync.Map
	runningJobRunCancel  sync.Map
	storageInstance      storage.IStorage
	storageMu            sync.RWMutex
	cancelledJobs        sync.Map
	pendingJobReports    sync.Map
	pendingStepReports   sync.Map
)

// SetStorage 设置 Agent 使用的对象存储客户端
func SetStorage(st storage.IStorage) {
	storageMu.Lock()
	defer storageMu.Unlock()
	storageInstance = st
}

// StartWorker starts the task queue worker. When execManager is non-nil, step run tasks
// are executed via executor.Manager (events go through Outbox when SetEventPublisher is used);
// otherwise the legacy executeStepRun (shell only) is used.
func StartWorker(
	workerCtx context.Context,
	agentConf *config.AgentConfig,
	grpcClient *grpc.ClientWrapper,
	execManager *executor.Manager,
) (nova.TaskQueue, error) {
	if agentConf == nil {
		return nil, nil
	}
	if err := ValidateExecutionConfig(agentConf); err != nil {
		return nil, err
	}
	cfg := agentConf.MessageQueue.Kafka
	if cfg.BootstrapServers == "" {
		return nil, nil
	}
	queueCfg := agentConf.TaskQueue
	delaySlotDuration := time.Duration(queueCfg.DelaySlotDuration) * time.Second
	if agentConf.Agent.ID == "" {
		return nil, fmt.Errorf("agent must register before starting workers")
	}
	options := []nova.QueueOption{
		nova.WithTopicPrefix(taskqueue.AgentTopicPrefix(agentConf.Agent.ID)),
		nova.WithGroupID("arcentra-agent-" + agentConf.Agent.ID),
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
		if workerCtx.Err() != nil {
			return workerCtx.Err()
		}
		switch task.Type {
		case taskqueue.TaskTypeJobRun:
			var payload taskqueue.JobRunTaskPayload
			if err := sonic.Unmarshal(task.Payload, &payload); err != nil {
				log.Warnw("discarding malformed job task", "error", err)
				return nil
			}
			if payload.AgentID != agentConf.Agent.ID {
				log.Warnw("discarding job addressed to a different agent", "jobRunID", payload.JobRunID)
				return nil
			}
			log.Infow("received job run task",
				"jobRunId", payload.JobRunID,
				"jobName", payload.JobName,
				"steps", len(payload.Steps),
			)
			return acknowledgeCompleted(executeJobRun(ctx, agentConf, grpcClient, execManager, &payload))
		case taskqueue.TaskTypeStepRun:
			var payload taskqueue.StepRunTaskPayload
			if err := sonic.Unmarshal(task.Payload, &payload); err != nil {
				log.Warnw("discarding malformed step task", "error", err)
				return nil
			}
			if payload.AgentID != agentConf.Agent.ID {
				log.Warnw("discarding step addressed to a different agent", "stepRunID", payload.StepRunID)
				return nil
			}
			log.Infow("received step run task",
				"stepRunId", payload.StepRunID,
				"jobName", payload.JobName,
				"stepName", payload.StepName,
			)
			if execManager != nil {
				return acknowledgeCompleted(executeStepRunViaExecutor(ctx, agentConf, grpcClient, execManager, &payload))
			}
			return acknowledgeCompleted(executeStepRun(ctx, agentConf, grpcClient, &payload))
		default:
			log.Debugw("unknown task type", "type", task.Type)
			return nil
		}
	})

	controlOptions := append([]nova.QueueOption{}, options...)
	controlOptions = append(controlOptions, nova.WithTopicPrefix(taskqueue.AgentControlTopicPrefix(agentConf.Agent.ID)), nova.WithGroupID("arcentra-agent-control-"+agentConf.Agent.ID))
	control, err := nova.NewTaskQueue(controlOptions...)
	if err != nil {
		_ = queue.Stop()
		return nil, fmt.Errorf("create control queue: %w", err)
	}
	if err := control.Start(nova.HandlerFunc(func(_ context.Context, task *nova.Task) error {
		if task == nil || task.Type != taskqueue.TaskTypeJobCancel {
			return nil
		}
		var payload taskqueue.JobCancelTaskPayload
		if err := sonic.Unmarshal(task.Payload, &payload); err != nil {
			return err
		}
		if payload.AgentID != agentConf.Agent.ID || payload.JobRunID == "" {
			return fmt.Errorf("invalid cancellation target")
		}
		CancelJobRun(payload.JobRunID)
		return nil
	})); err != nil {
		_ = queue.Stop()
		_ = control.Stop()
		return nil, fmt.Errorf("start control queue: %w", err)
	}
	if err := queue.Start(handler); err != nil {
		_ = control.Stop()
		_ = queue.Stop()
		return nil, fmt.Errorf("start task queue: %w", err)
	}
	return &workerQueues{TaskQueue: queue, control: control}, nil
}

type workerQueues struct {
	nova.TaskQueue
	control nova.TaskQueue
}

func (q *workerQueues) Stop() error {
	// 工作队列先停止，使已开始的任务能在控制队列和 RPC 关闭前上报结束状态
	return errors.Join(q.TaskQueue.Stop(), q.control.Stop())
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
	ctx context.Context, agentConf *config.AgentConfig, grpcClient *grpc.ClientWrapper,
	execManager *executor.Manager, payload *taskqueue.StepRunTaskPayload,
	override ...executor.Executor,
) error {
	if payload != nil {
		if pending, ok := pendingStepReports.Load(payload.StepRunID); ok {
			return pending.(*executionReceipt).deliver(&pendingStepReports, payload.StepRunID)
		}
	}
	if payload == nil || payload.StepRunID == "" {
		return grpcstatus.Error(codes.InvalidArgument, "missing step run ID")
	}
	stepCtx, cancel := context.WithCancel(ctx)
	if _, loaded := runningStepRunCancel.LoadOrStore(payload.StepRunID, cancel); loaded {
		cancel()
		return fmt.Errorf("step run %s is already running", payload.StepRunID)
	}
	defer func() { runningStepRunCancel.Delete(payload.StepRunID); cancel() }()
	start := time.Now()
	defaultTimeout := 0
	if agentConf != nil {
		defaultTimeout = agentConf.Agent.JobTimeout
	}
	if timeout := parseTimeout(payload.Timeout, defaultTimeout); timeout > 0 {
		var timeoutCancel context.CancelFunc
		stepCtx, timeoutCancel = context.WithTimeout(stepCtx, timeout)
		defer timeoutCancel()
	}
	if err := claimStepRun(stepCtx, grpcClient, agentConf, payload.StepRunID, start); err != nil {
		return err
	}
	req := PayloadToExecutionRequest(payload, defaultTimeout)
	if agentConf != nil {
		req.AgentID = agentConf.Agent.ID
		req.Env = mergeEnv(agentConf.Agent.EnvVars, payload.Env)
	}
	if strings.TrimSpace(req.Workspace) == "" {
		runID, jobID := payload.PipelineRunID, payload.JobID
		if runID == "" {
			runID = "steps"
		}
		if jobID == "" {
			jobID = payload.StepRunID
		}
		workspace, err := prepareWorkspace(agentConf, &taskqueue.JobRunTaskPayload{PipelineRunID: runID, JobRunID: jobID})
		if err != nil {
			end := time.Now().Unix()
			return retainStepReport(payload.StepRunID, err, func() error {
				return reportStepRunStatus(grpcClient, agentConf, payload.StepRunID, steprunv1.StepRunStatus_STEP_RUN_STATUS_FAILED, 1, err.Error(), start, end, nil)
			})
		}
		req.Workspace = workspace
	}
	if execManager == nil {
		execManager = executor.NewExecutorManager()
		execManager.Register(executor.NewShellExecutor())
	}
	var result *executor.ExecutionResult
	var execErr error
	if len(override) > 0 && override[0] != nil {
		result, execErr = execManager.ExecuteWithExecutor(stepCtx, req, override[0])
	} else {
		result, execErr = execManager.Execute(stepCtx, req)
	}
	metrics := map[string]string{"workspace": req.Workspace}
	if result != nil {
		metrics["executor"] = result.ExecutorName
		metrics["outputBytes"] = fmt.Sprint(len(result.Output) + len(result.ErrorOutput))
		if value, ok := result.Metadata["outputBytes"]; ok {
			metrics["outputBytes"] = fmt.Sprint(value)
		}
	}
	status, exitCode, errMsg := resultToStepRunStatus(stepCtx, result, execErr)
	end := time.Now().Unix()
	if execErr == nil && status != steprunv1.StepRunStatus_STEP_RUN_STATUS_SUCCESS {
		execErr = fmt.Errorf("step failed: %s", errMsg)
	}
	return retainStepReport(payload.StepRunID, execErr, func() error {
		return reportStepRunStatus(grpcClient, agentConf, payload.StepRunID, status, exitCode, errMsg, start, end, metrics)
	})
}

func resultToStepRunStatus(ctx context.Context, result *executor.ExecutionResult, execErr error) (steprunv1.StepRunStatus, int32, string) {
	executionCause := ctx.Err()
	if executionCause == nil {
		executionCause = executor.ExecutionContextCause(execErr)
	}
	if errors.Is(executionCause, context.DeadlineExceeded) {
		return steprunv1.StepRunStatus_STEP_RUN_STATUS_TIMEOUT, 1, "timeout"
	}
	if errors.Is(executionCause, context.Canceled) {
		return steprunv1.StepRunStatus_STEP_RUN_STATUS_CANCELLED, 1, "cancelled"
	}
	if errors.Is(execErr, executor.ErrBuildLogPersistence) {
		exitCode := int32(1)
		if result != nil && result.ExitCode != 0 {
			exitCode = result.ExitCode
		}
		return steprunv1.StepRunStatus_STEP_RUN_STATUS_FAILED, exitCode, execErr.Error()
	}
	if errors.Is(execErr, context.DeadlineExceeded) {
		return steprunv1.StepRunStatus_STEP_RUN_STATUS_TIMEOUT, 1, "timeout"
	}
	if errors.Is(execErr, context.Canceled) {
		return steprunv1.StepRunStatus_STEP_RUN_STATUS_CANCELLED, 1, "cancelled"
	}
	if result == nil {
		errMsg := ""
		if execErr != nil {
			errMsg = execErr.Error()
		}
		return steprunv1.StepRunStatus_STEP_RUN_STATUS_FAILED, 1, errMsg
	}
	if result.Success && execErr == nil {
		return steprunv1.StepRunStatus_STEP_RUN_STATUS_SUCCESS, result.ExitCode, ""
	}
	errMsg := result.Error
	if errMsg == "" && execErr != nil {
		errMsg = execErr.Error()
	}
	return steprunv1.StepRunStatus_STEP_RUN_STATUS_FAILED, result.ExitCode, errMsg
}

// CancelStepRun 取消正在执行的步骤
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

// CancelJobRun 取消作业并保存待执行作业的取消信号
func CancelJobRun(jobRunID string) bool {
	if jobRunID == "" {
		return false
	}
	now := time.Now()
	cancelledJobs.Range(func(key, value any) bool {
		if expires, ok := value.(time.Time); ok && now.After(expires) {
			cancelledJobs.Delete(key)
		}
		return true
	})
	cancelledJobs.Store(jobRunID, now.Add(24*time.Hour))
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

// executeJobRun 顺序执行作业步骤，完整报告未执行步骤与作业的终态
func executeJobRun(ctx context.Context, agentConf *config.AgentConfig, grpcClient *grpc.ClientWrapper, execManager *executor.Manager, payload *taskqueue.JobRunTaskPayload) error {
	if payload != nil {
		if pending, ok := pendingJobReports.Load(payload.JobRunID); ok {
			return pending.(*executionReceipt).deliver(&pendingJobReports, payload.JobRunID)
		}
	}
	if payload == nil || payload.JobRunID == "" {
		return grpcstatus.Error(codes.InvalidArgument, "missing job run ID")
	}
	jobCtx, cancel := context.WithCancel(ctx)
	if _, loaded := runningJobRunCancel.LoadOrStore(payload.JobRunID, cancel); loaded {
		cancel()
		return fmt.Errorf("job run %s is already running", payload.JobRunID)
	}
	defer func() { runningJobRunCancel.Delete(payload.JobRunID); cancel() }()
	if expires, ok := cancelledJobs.Load(payload.JobRunID); ok && time.Now().Before(expires.(time.Time)) {
		cancel()
	}
	defaultTimeout := 0
	if agentConf != nil {
		defaultTimeout = agentConf.Agent.JobTimeout
	}
	if timeout := parseTimeout(payload.Timeout, defaultTimeout); timeout > 0 {
		var timeoutCancel context.CancelFunc
		jobCtx, timeoutCancel = context.WithTimeout(jobCtx, timeout)
		defer timeoutCancel()
	}
	start := time.Now()
	if jobCtx.Err() == nil {
		if err := reportJobRunStatus(grpcClient, agentConf, payload.JobRunID, agentv1.AgentStatus_AGENT_STATUS_BUSY, int32(steprunv1.StepRunStatus_STEP_RUN_STATUS_RUNNING), "", start, 0); err != nil {
			return fmt.Errorf("claim job run: %w", err)
		}
	}
	workspace, jobErr := prepareWorkspace(agentConf, payload)
	if jobErr == nil {
		jobErr = handleSourceClone(jobCtx, payload.Source, workspace)
	}
	storageMu.RLock()
	st := storageInstance
	storageMu.RUnlock()
	if jobErr == nil {
		jobErr = handleArtifactDownload(jobCtx, payload.ArtifactURIs, workspace, st)
	}
	artifacts := &artifactExecutor{job: payload, storage: st, uploaded: make(map[string]string)}
	for _, step := range payload.Steps {
		if jobCtx.Err() != nil || jobErr != nil {
			status := steprunv1.StepRunStatus_STEP_RUN_STATUS_SKIPPED
			reason := "previous step or workspace preparation failed"
			if jobCtx.Err() != nil {
				status = steprunv1.StepRunStatus_STEP_RUN_STATUS_CANCELLED
				reason = jobCtx.Err().Error()
			}
			end := time.Now().Unix()
			_ = retainStepReport(step.StepRunID, nil, func() error {
				return reportStepRunStatus(grpcClient, agentConf, step.StepRunID, status, 0, reason, start, end, nil)
			})
			continue
		}
		env := mergeEnv(payload.Env, step.Env)
		allowed, conditionErr := evaluateStepCondition(step.When, payload.JobName, step.Name, env, payload.Variables)
		if conditionErr != nil {
			conditionErr = redactJobError(payload, conditionErr)
			stepStart := time.Now()
			_ = retainStepReport(step.StepRunID, conditionErr, func() error {
				return reportStepRunStatus(grpcClient, agentConf, step.StepRunID, steprunv1.StepRunStatus_STEP_RUN_STATUS_FAILED, 1, conditionErr.Error(), stepStart, stepStart.Unix(), nil)
			})
			if !step.ContinueOnError {
				jobErr = conditionErr
			}
			continue
		}
		if !allowed {
			stepStart := time.Now()
			_ = retainStepReport(step.StepRunID, nil, func() error {
				return reportStepRunStatus(grpcClient, agentConf, step.StepRunID, steprunv1.StepRunStatus_STEP_RUN_STATUS_SKIPPED, 0, "condition evaluated to false", stepStart, stepStart.Unix(), nil)
			})
			continue
		}
		stepPayload := &taskqueue.StepRunTaskPayload{PipelineID: payload.PipelineID, PipelineRunID: payload.PipelineRunID, JobID: payload.JobRunID, JobName: payload.JobName, StepName: step.Name, StepIndex: step.StepIndex, StepRunID: step.StepRunID, Uses: step.Uses, Action: step.Action, Args: step.Args, Env: env, Workspace: workspace, Timeout: step.Timeout, AgentID: payload.AgentID}
		var stepErr error
		if strings.EqualFold(strings.TrimSpace(step.Uses), "builtin:artifacts") {
			stepErr = executeStepRunViaExecutor(jobCtx, agentConf, grpcClient, execManager, stepPayload, artifacts)
		} else if execManager != nil {
			stepErr = executeStepRunViaExecutor(jobCtx, agentConf, grpcClient, execManager, stepPayload)
		} else {
			stepErr = executeStepRun(jobCtx, agentConf, grpcClient, stepPayload)
		}
		if pending, ok := pendingStepReports.Load(step.StepRunID); ok {
			// 上报失败不改变执行结果；作业终态上报前会重试所有待交付步骤
			stepErr = pending.(*executionReceipt).executionErr
		}
		if stepErr != nil && !step.ContinueOnError {
			jobErr = fmt.Errorf("step %s failed: %w", step.Name, stepErr)
		}
	}
	if jobCtx.Err() != nil {
		jobErr = jobCtx.Err()
	}
	status := steprunv1.StepRunStatus_STEP_RUN_STATUS_SUCCESS
	switch {
	case errors.Is(executor.ExecutionContextCause(jobErr), context.DeadlineExceeded):
		status = steprunv1.StepRunStatus_STEP_RUN_STATUS_TIMEOUT
	case errors.Is(executor.ExecutionContextCause(jobErr), context.Canceled):
		status = steprunv1.StepRunStatus_STEP_RUN_STATUS_CANCELLED
	case errors.Is(jobErr, executor.ErrBuildLogPersistence):
		status = steprunv1.StepRunStatus_STEP_RUN_STATUS_FAILED
	case errors.Is(jobErr, context.DeadlineExceeded):
		status = steprunv1.StepRunStatus_STEP_RUN_STATUS_TIMEOUT
	case errors.Is(jobErr, context.Canceled):
		status = steprunv1.StepRunStatus_STEP_RUN_STATUS_CANCELLED
	case jobErr != nil:
		status = steprunv1.StepRunStatus_STEP_RUN_STATUS_FAILED
	}
	jobErr = redactJobError(payload, jobErr)
	errMsg := ""
	if jobErr != nil {
		errMsg = jobErr.Error()
	}
	end := time.Now().Unix()
	receipt := &executionReceipt{executionErr: jobErr, report: func() error {
		for _, step := range payload.Steps {
			if pending, ok := pendingStepReports.Load(step.StepRunID); ok {
				if err := pending.(*executionReceipt).deliver(&pendingStepReports, step.StepRunID); err != nil {
					var completed *completedExecutionError
					if !errors.As(err, &completed) {
						return err
					}
				}
			}
		}
		return reportJobRunStatus(grpcClient, agentConf, payload.JobRunID, agentv1.AgentStatus_AGENT_STATUS_IDLE, int32(status), errMsg, start, end, artifacts.uploaded)
	}}
	pendingJobReports.Store(payload.JobRunID, receipt)
	return receipt.deliver(&pendingJobReports, payload.JobRunID)
}

func prepareWorkspace(conf *config.AgentConfig, payload *taskqueue.JobRunTaskPayload) (string, error) {
	workspace := strings.TrimSpace(payload.Workspace)
	if workspace == "" {
		base := "workspaces"
		if conf != nil && conf.Agent.WorkspaceDir != "" {
			base = conf.Agent.WorkspaceDir
		}
		// 运行标识不能改变工作目录的层级
		for _, id := range []string{payload.PipelineRunID, payload.JobRunID} {
			if id == "" || filepath.Base(id) != id || id == "." || id == ".." {
				return "", fmt.Errorf("invalid run ID for workspace")
			}
		}
		workspace = filepath.Join(base, payload.PipelineRunID, payload.JobRunID)
	}
	absolute, err := filepath.Abs(workspace)
	if err != nil {
		return "", fmt.Errorf("resolve workspace: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o755); err != nil {
		return "", fmt.Errorf("create workspace: %w", err)
	}
	return absolute, nil
}

func evaluateStepCondition(condition, jobName, stepName string, env, variables map[string]string) (bool, error) {
	if strings.TrimSpace(condition) == "" {
		return true, nil
	}
	values := make(map[string]any, len(env)+3)
	for key, value := range env {
		values[key] = value
	}
	values["env"] = env
	values["variables"] = variables
	values["job"] = map[string]any{"name": jobName}
	values["step"] = map[string]any{"name": stepName}
	program, err := expr.Compile(condition, expr.Env(values), expr.AsBool())
	if err != nil {
		return false, fmt.Errorf("compile step condition: %w", err)
	}
	result, err := expr.Run(program, values)
	if err != nil {
		return false, fmt.Errorf("evaluate step condition: %w", err)
	}
	allowed, ok := result.(bool)
	if !ok {
		return false, fmt.Errorf("step condition must return bool")
	}
	return allowed, nil
}

// RunningStepRunsCount 返回当前执行中的步骤数
func RunningStepRunsCount() int32 {
	var count int32
	runningStepRunCancel.Range(func(_, _ any) bool { count++; return true })
	return count
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
	artifactURIs ...map[string]string,
) error {
	if grpcClient == nil || grpcClient.AgentClient == nil {
		return nil
	}
	ctx, cancel := grpcClient.WithTimeoutAndAuth(context.Background())
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
	if len(artifactURIs) > 0 {
		req.ArtifactUris = artifactURIs[0]
	}
	ctx = withExecutionClaim(ctx, "job", jobRunID)
	resp, err := grpcClient.AgentClient.ReportJobRunStatus(ctx, req)
	if err != nil {
		return err
	}
	if resp == nil || !resp.Success {
		return fmt.Errorf("job status report rejected")
	}
	if status >= int32(steprunv1.StepRunStatus_STEP_RUN_STATUS_SUCCESS) {
		completeExecutionClaim("job", jobRunID)
	}
	return nil
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

func executeStepRun(ctx context.Context, agentConf *config.AgentConfig, grpcClient *grpc.ClientWrapper, payload *taskqueue.StepRunTaskPayload) error {
	manager := executor.NewExecutorManager()
	manager.Register(executor.NewShellExecutor())
	return executeStepRunViaExecutor(ctx, agentConf, grpcClient, manager, payload)
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
	ctx = withExecutionClaim(ctx, "step", stepRunID)
	resp, err := grpcClient.AgentClient.ReportStepRunStatus(ctx, req)
	if err != nil {
		return err
	}
	if resp == nil || !resp.Success {
		return fmt.Errorf("step status report rejected")
	}
	if status >= steprunv1.StepRunStatus_STEP_RUN_STATUS_SUCCESS {
		completeExecutionClaim("step", stepRunID)
	}
	return nil
}

func buildCommandFromPayload(payload *taskqueue.StepRunTaskPayload) string {
	if payload == nil {
		return ""
	}
	return executor.BuildCommandFromStepArgs(&executor.StepInfo{Args: payload.Args})
}

func parseTimeout(raw string, defaultSeconds int) time.Duration {
	return executor.ParseTimeout(raw, defaultSeconds)
}

// handleSourceClone clones the source repository into the workspace directory
// when the job payload contains a SourcePayload with type "git".
func handleSourceClone(ctx context.Context, src *taskqueue.SourcePayload, workspace string) error {
	if src == nil || src.Type == "" {
		return nil
	}
	if !strings.EqualFold(src.Type, "git") {
		return fmt.Errorf("unsupported source type %q", src.Type)
	}
	if src.Repo == "" {
		return fmt.Errorf("git source repository is required")
	}
	if repoURL, err := url.Parse(src.Repo); err == nil && repoURL.User != nil {
		return fmt.Errorf("git source URL must not contain credentials; use source.auth instead")
	}
	if src.Commit != "" && !regexp.MustCompile(`^[a-fA-F0-9]{7,64}$`).MatchString(src.Commit) {
		return fmt.Errorf("source commit must be a hexadecimal commit ID")
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return err
	}
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	username, password := src.Auth["username"], src.Auth["password"]
	if password == "" {
		password = src.Auth["token"]
		if password != "" && username == "" {
			username = "oauth2"
		}
	}
	if password != "" {
		// 认证内容仅放入子进程环境，避免写进命令行、仓库配置或脚本文件
		file, err := os.CreateTemp("", "arcentra-askpass-*")
		if err != nil {
			return err
		}
		name := file.Name()
		defer os.Remove(name)
		script := "#!/bin/sh\ncase \"$1\" in *Username*) printf '%s\\n' \"$ARCENTRA_GIT_USERNAME\" ;; *) printf '%s\\n' \"$ARCENTRA_GIT_PASSWORD\" ;; esac\n"
		if _, err := file.WriteString(script); err != nil {
			_ = file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		if err := os.Chmod(name, 0o700); err != nil {
			return err
		}
		env = append(env, "GIT_ASKPASS="+name, "ARCENTRA_GIT_USERNAME="+username, "ARCENTRA_GIT_PASSWORD="+password)
	}
	run := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir, cmd.Env = workspace, env
		cmd.WaitDelay = 2 * time.Second
		output, err := cmd.CombinedOutput()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			message := string(output)
			for _, secret := range []string{password, src.Auth["token"]} {
				if secret != "" {
					message = strings.ReplaceAll(message, secret, "[REDACTED]")
				}
			}
			return fmt.Errorf("git source operation failed: %s: %w", message, err)
		}
		return nil
	}
	args := []string{"clone", "--depth", "1"}
	if src.Branch != "" {
		args = append(args, "--branch", src.Branch)
	}
	args = append(args, "--", src.Repo, ".")
	if err := run(args...); err != nil {
		return err
	}
	if src.Commit != "" {
		if err := run("fetch", "--depth", "1", "origin", src.Commit); err != nil {
			return err
		}
		if err := run("checkout", "--detach", src.Commit); err != nil {
			return err
		}
	}
	return nil
}

// retainStepReport 缓存所有步骤终态上报，避免临时 RPC 故障丢失跳过或失败结果
func retainStepReport(stepRunID string, executionErr error, report func() error) error {
	receipt := &executionReceipt{executionErr: executionErr, report: report}
	pendingStepReports.Store(stepRunID, receipt)
	return receipt.deliver(&pendingStepReports, stepRunID)
}

// executionReceipt 保留终态上报以避免队列重试重复执行 Shell
// 持久化重放仍需要控制面的运行尝试标识
type executionReceipt struct {
	executionErr error
	report       func() error
}

func (r *executionReceipt) deliver(pending *sync.Map, key string) error {
	if err := r.report(); err != nil {
		return err
	}
	pending.Delete(key)
	if r.executionErr != nil {
		return &completedExecutionError{r.executionErr}
	}
	return nil
}

type completedExecutionError struct{ error }

func (e *completedExecutionError) Unwrap() error { return e.error }
func acknowledgeCompleted(err error) error {
	if err == nil {
		return nil
	}
	var completed *completedExecutionError
	if errors.As(err, &completed) {
		return nil
	}
	switch grpcstatus.Code(err) {
	case codes.FailedPrecondition, codes.PermissionDenied, codes.InvalidArgument, codes.NotFound:
		log.Warnw("discarding rejected execution task", "error", err)
		return nil
	}
	return err
}

// ValidateExecutionConfig 拒绝无法实际强制执行的隔离或安全配置
func ValidateExecutionConfig(conf *config.AgentConfig) error {
	if conf == nil {
		return fmt.Errorf("agent configuration is required")
	}
	if strings.ToLower(strings.TrimSpace(conf.Agent.Mode)) != "baremetal" || conf.Agent.Sandbox.Enable {
		return fmt.Errorf("this agent supports only explicit agent.mode=baremetal with agent.sandbox.enable=false; sandbox execution is not implemented")
	}
	if len(conf.Agent.DeniedCommands) > 0 || len(conf.Agent.ResourceLimits) > 0 {
		return fmt.Errorf("baremetal executor cannot enforce deniedCommands or resourceLimits; use a dedicated trusted host and remove unsupported restrictions explicitly")
	}
	if conf.Agent.EnableDocker {
		return fmt.Errorf("Docker execution is not implemented by this worker")
	}
	return nil
}
