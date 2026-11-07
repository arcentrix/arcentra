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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	steprunv1 "github.com/arcentrix/arcentra/api/steprun/v1"
	"github.com/arcentrix/arcentra/internal/agent/config"
	"github.com/arcentrix/arcentra/internal/agent/sandboxexec"
	"github.com/arcentrix/arcentra/internal/shared/executor"
	"github.com/arcentrix/arcentra/internal/shared/grpc"
	"github.com/arcentrix/arcentra/internal/shared/storage"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/nova"
	"github.com/arcentrix/arcentra/pkg/safe"
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
	storageReference     taskqueue.ArtifactReference
	storageMu            sync.RWMutex
	cancelledJobs        sync.Map
	pendingJobReports    sync.Map
	pendingStepReports   sync.Map
)

// SetStorage 设置 Agent 使用的对象存储客户端
func SetStorage(st storage.IStorage, reference ...taskqueue.ArtifactReference) {
	storageMu.Lock()
	defer storageMu.Unlock()
	storageInstance = st
	storageReference = taskqueue.ArtifactReference{}
	if len(reference) > 0 {
		storageReference = reference[0]
	}
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
	codec, err := taskqueue.ResolveMessageCodec(queueCfg.MessageFormat, queueCfg.MessageCodec)
	if err != nil {
		return nil, fmt.Errorf("configure agent task queue: %w", err)
	}
	delaySlotDuration := time.Duration(queueCfg.DelaySlotDuration) * time.Second
	if agentConf.Agent.ID == "" {
		return nil, fmt.Errorf("agent must register before starting workers")
	}
	if grpcClient == nil || grpcClient.AgentClient == nil {
		return nil, fmt.Errorf("agent must connect before starting workers")
	}
	var incarnation *rerunIncarnation
	if agentConf.Agent.Sandbox.Containerd.TerminalRerunEnabled {
		incarnation, err = newRerunIncarnation(1024)
		if err != nil {
			return nil, err
		}
	}
	reportDirectory := filepath.Join(agentConf.Agent.WorkspaceDir, ".terminal-report-outbox")
	reports, err := newTerminalOutbox(workerCtx, reportDirectory, agentConf.Agent.ID, agentConf.Grpc.ServerAddr)
	if err != nil {
		return nil, err
	}
	started := false
	defer func() {
		if !started {
			_ = reports.close()
		}
	}()
	// 限定启动等待；暂时断线时仍启动消费，但处理器在重放成功前禁止新命令
	startupReplayCtx, cancelStartupReplay := context.WithTimeout(workerCtx, 10*time.Second)
	startupReplayErr := reports.replayBound(startupReplayCtx, grpcClient, agentConf)
	cancelStartupReplay()
	if startupReplayErr != nil {
		log.Warnw("terminal reports remain durably queued before worker startup")
	}
	if err := workerCtx.Err(); err != nil {
		return nil, err
	}
	offline, err := newOfflineRuntime(workerCtx, agentConf, grpcClient)
	if err != nil {
		return nil, err
	}
	if offline != nil {
		workerCtx = withOfflineJobRuntime(workerCtx, offline)
		defer func() {
			if !started {
				_ = offline.Close(context.Background())
			}
		}()
	}
	options := []nova.QueueOption{
		nova.WithMessageCodec(codec),
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
	queue, err := nova.NewTaskQueue(options...)
	if err != nil {
		return nil, taskqueue.WrapStartupError("create task queue", err)
	}

	handler := newWorkerHandler(workerCtx, agentConf, grpcClient, execManager, reports, incarnation)
	handler = observeAcceptanceWorkerHandler(workerCtx, handler)

	controlOptions := append([]nova.QueueOption{}, options...)
	controlOptions = append(controlOptions, nova.WithTopicPrefix(taskqueue.AgentControlTopicPrefix(agentConf.Agent.ID)), nova.WithGroupID("arcentra-agent-control-"+agentConf.Agent.ID))
	control, err := nova.NewTaskQueue(controlOptions...)
	if err != nil {
		_ = queue.Stop()
		return nil, taskqueue.WrapStartupError("create control queue", err)
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
		return nil, taskqueue.WrapStartupError("start control queue", err)
	}
	if err := queue.Start(handler); err != nil {
		_ = control.Stop()
		_ = queue.Stop()
		return nil, taskqueue.WrapStartupError("start task queue", err)
	}
	runCtx, cancelReports := context.WithCancel(workerCtx)
	runCtx = observeAcceptanceWorkerReplay(runCtx)
	result := &workerQueues{incarnation: incarnation, offline: offline, TaskQueue: queue, control: control, reports: reports, cancelReports: cancelReports, reportsDone: make(chan struct{})}
	started = true
	safe.Go(func() {
		defer close(result.reportsDone)
		timer := time.NewTicker(time.Second)
		defer timer.Stop()
		warned := false
		lastQuarantined, lastUnreviewed := -1, -1
		for {
			if err := reports.replayBound(runCtx, grpcClient, agentConf); err != nil && runCtx.Err() == nil {
				if !warned {
					log.Warnw("terminal reports remain durably queued for retry")
					warned = true
				}
			} else {
				warned = false
			}
			quarantined, unreviewed := reports.quarantineCounts()
			if quarantined > 0 && (quarantined != lastQuarantined || unreviewed != lastUnreviewed) {
				log.Warnw("terminal conflicts retained for offline operator inspection; results were not accepted", "quarantined", quarantined, "unreviewed", unreviewed)
			}
			lastQuarantined, lastUnreviewed = quarantined, unreviewed
			select {
			case <-runCtx.Done():
				return
			case <-timer.C:
			}
		}
	})
	return result, nil
}

type workerQueues struct {
	incarnation *rerunIncarnation
	offline     *durableOfflineRuntime
	nova.TaskQueue
	control       nova.TaskQueue
	reports       *terminalOutbox
	cancelReports context.CancelFunc
	reportsDone   chan struct{}
	stopOnce      sync.Once
	stopErr       error
}

func (q *workerQueues) Stop() error {
	// 工作队列先停止，使已开始的任务能在控制队列和 RPC 关闭前上报结束状态
	q.stopOnce.Do(func() {
		if q.offline != nil {
			q.offline.BeginDrain()
		}
		q.stopErr = errors.Join(q.TaskQueue.Stop(), q.control.Stop())
		if q.cancelReports != nil {
			q.cancelReports()
		}
		if q.reportsDone != nil {
			<-q.reportsDone
		}
		if q.offline != nil {
			q.stopErr = errors.Join(q.stopErr, q.offline.Close(context.Background()))
		}
		if q.reports != nil {
			q.stopErr = errors.Join(q.stopErr, q.reports.close())
		}
	})
	return q.stopErr
}

func executeStepRunViaExecutor(
	ctx context.Context, agentConf *config.AgentConfig, grpcClient *grpc.ClientWrapper,
	execManager *executor.Manager, payload *taskqueue.StepRunTaskPayload,
	override ...executor.Executor,
) error {
	ctx = withTerminalSecrets(ctx, agentConf, nil, payload)
	if err := taskqueue.ValidateStandaloneRuntime(payload); err != nil {
		return grpcstatus.Error(codes.FailedPrecondition, err.Error())
	}
	if agentConf != nil && agentConf.Agent.Sandbox.Enable && len(override) == 0 {
		return grpcstatus.Error(codes.FailedPrecondition, "sandbox steps require their allocated job executor")
	}
	if payload != nil {
		if pending, ok := pendingStepReports.Load(receiptKey(ctx, "step", payload.StepRunID)); ok {
			return pending.(*executionReceipt).deliver(ctx, &pendingStepReports, receiptKey(ctx, "step", payload.StepRunID))
		}
	}
	if payload == nil || payload.StepRunID == "" {
		return grpcstatus.Error(codes.InvalidArgument, "missing step run ID")
	}
	if found, err := replayTerminalForTask(ctx, grpcClient, agentConf, "step", payload.StepRunID); found || err != nil {
		return err
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
	if err := validateStepExecution(payload.Uses, payload.Action, payload.Args, len(override) > 0 && override[0] != nil); err != nil {
		end := time.Now().Unix()
		return retainStepReport(ctx, payload.StepRunID, err, func(reportCtx context.Context) error {
			return reportStepRunStatus(reportCtx, grpcClient, agentConf, payload.StepRunID, steprunv1.StepRunStatus_STEP_RUN_STATUS_FAILED, 1, err.Error(), start, end, nil)
		})
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
			return retainStepReport(ctx, payload.StepRunID, err, func(reportCtx context.Context) error {
				return reportStepRunStatus(reportCtx, grpcClient, agentConf, payload.StepRunID, steprunv1.StepRunStatus_STEP_RUN_STATUS_FAILED, 1, err.Error(), start, end, nil)
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
	} else if strings.EqualFold(strings.TrimSpace(payload.Uses), "http") {
		// 显式 HTTP 步骤不可因 args.run 而被 ShellExecutor 抢先选择
		result, execErr = execManager.ExecuteWithExecutor(stepCtx, req, executor.NewHTTPExecutor(log.Logger{}))
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
	// 日志完成后再验证本地暂停能力，冻结终态之前决定纯命令失败标志
	// 后续回执重放绝不重新观察能力或改变已经持久化的终态字节
	pureStepExit := false
	if attempt := stepAttemptFromContext(ctx); attempt != nil && status == steprunv1.StepRunStatus_STEP_RUN_STATUS_FAILED {
		if generation := generationFromContext(ctx); generation != nil {
			if code, eligible := sandboxexec.RetryCommandExitCode(execErr, generation.payload.Retry.AllowedExitCodes); eligible && code == exitCode {
				if boundaryErr := attempt.session.HasPausedStepBoundary(stepCtx, attempt.admission); boundaryErr != nil {
					execErr = errors.Join(execErr, fmt.Errorf("failed-step continuation refused: %w", boundaryErr))
					errMsg = execErr.Error()
				} else {
					pureStepExit = true
				}
			}
		}
	}
	return retainStepReport(ctx, payload.StepRunID, execErr, func(reportCtx context.Context) error {
		if stepAttemptFromContext(reportCtx) != nil {
			reportCtx = context.WithValue(reportCtx, generationPureExitKey{}, pureStepExit)
		} else if generation := generationFromContext(reportCtx); generation != nil && generation.retryableStep(payload.StepRunID) && status == steprunv1.StepRunStatus_STEP_RUN_STATUS_FAILED {
			code, eligible := sandboxexec.RetryCommandExitCode(execErr, generation.payload.Retry.AllowedExitCodes)
			if eligible && code == exitCode {
				reportCtx = context.WithValue(reportCtx, generationPureExitKey{}, true)
			}
		}
		return reportStepRunStatus(reportCtx, grpcClient, agentConf, payload.StepRunID, status, exitCode, errMsg, start, end, metrics)
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
	if payload != nil && payload.SourceExpectation != nil {
		return grpcstatus.Error(codes.FailedPrecondition, "protected rerun requires dedicated admission")
	}
	return executeJobRunBody(ctx, agentConf, grpcClient, execManager, payload, nil)
}

// executeJobRunBody preserves the ordinary execution/report ordering. Only a
// manager-owned, consumed-once token may skip the generic first claim.
func executeJobRunBody(ctx context.Context, agentConf *config.AgentConfig, grpcClient *grpc.ClientWrapper, execManager *executor.Manager, payload *taskqueue.JobRunTaskPayload, token *rerunStartToken) error {
	if token != nil {
		claim, err := token.consume(payload)
		if err != nil {
			return err
		}
		ctx = context.WithValue(ctx, receiptJobClaimContextKey{}, receiptJobClaim{runID: payload.JobRunID, claimID: claim})
	} else if payload != nil && payload.SourceExpectation != nil {
		return errRerunIncarnationInvalid
	}

	ctx = withTerminalSecrets(ctx, agentConf, payload, nil)
	if payload != nil {
		if pending, ok := pendingJobReports.Load(receiptKey(ctx, "job", payload.JobRunID)); ok {
			return deliverJobReceipt(ctx, payload, pending.(*executionReceipt))
		}
	}
	if payload == nil || payload.JobRunID == "" {
		return grpcstatus.Error(codes.InvalidArgument, "missing job run ID")
	}
	if found, err := replayTerminalForTask(ctx, grpcClient, agentConf, "job", payload.JobRunID); found || err != nil {
		return err
	}
	localRuntimeErr, runtimeValidationErr := offlineJobValidation(ctx, agentConf, payload)
	if runtimeValidationErr != nil {
		var unavailable *terminalDeliveryError
		if errors.As(runtimeValidationErr, &unavailable) {
			return runtimeValidationErr
		}
		return grpcstatus.Error(codes.FailedPrecondition, runtimeValidationErr.Error())
	}
	if payload.Runtime != nil {
		ctx = context.WithValue(ctx, runtimeBindingContextKey{}, payload.Runtime.BindingDigest)
	}
	ctx = withJobExecutionClaim(ctx, payload.JobRunID)
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
	storageMu.RLock()
	st, reference := storageInstance, storageReference
	storageMu.RUnlock()
	if token != nil {
		st, reference = nil, taskqueue.ArtifactReference{}
		if token.artifacts != nil {
			st, reference = token.artifacts.instance, token.artifacts.reference
		}
	}
	artifacts := &artifactExecutor{job: payload, storage: st, reference: reference, uploaded: make(map[string]string)}
	start := time.Now()
	// 即使控制消息先到，也先确认原作业认领；已终态且未认领的任务不能产生收据
	if token == nil {
		if err := reportJobRunStatus(ctx, grpcClient, agentConf, payload.JobRunID, agentv1.AgentStatus_AGENT_STATUS_BUSY, int32(steprunv1.StepRunStatus_STEP_RUN_STATUS_RUNNING), "", start, 0, artifacts); err != nil {
			// 确定拒绝且即将消费的任务没有执行权，不保留无用认领；不确定响应仍复用原身份
			switch grpcstatus.Code(err) {
			case codes.FailedPrecondition, codes.PermissionDenied, codes.InvalidArgument, codes.NotFound:
				completeExecutionClaim(ctx, "job", payload.JobRunID)
			}
			return fmt.Errorf("claim job run: %w", err)
		}
	}
	jobErr := localRuntimeErr
	if jobErr == nil {
		jobErr = validateJobExecution(payload)
	}
	if jobErr == nil {
		jobErr = validateJobSourceBinding(payload)
	}
	workspace := ""
	var initialCheckout *agentv1.SourceCheckout
	var session *offlineJobSession
	runtimeCompletionRequired := false
	if jobErr == nil && payload.Runtime != nil && payload.Retry != nil {
		var generated generationJobResult
		if payload.Retry.Mode == "failed_step" {
			generated = executeContainerFailedSteps(ctx, jobCtx, agentConf, grpcClient, execManager, payload, st, reference, start)
		} else {
			generated = executeContainerGenerations(ctx, jobCtx, agentConf, grpcClient, execManager, payload, st, reference, start)
		}
		if generated.uncertain {
			return generated.err
		}
		ctx, artifacts, initialCheckout, session, jobErr = generated.ctx, generated.artifacts, generated.checkout, generated.session, generated.err
		runtimeCompletionRequired = generated.journalRequired
	} else {
		if jobErr == nil {
			if payload.Runtime != nil {
				var allocated offlineSession
				allocated, jobErr = offlineRuntimeFromContext(ctx).Allocate(jobCtx, payload, executionClaimID(ctx, "job", payload.JobRunID))
				if allocated != nil {
					session = &offlineJobSession{offlineSession: allocated}
					workspace = allocated.Workspace()
				}
				if jobErr == nil && (allocated == nil || workspace == "" || !filepath.IsAbs(workspace)) {
					jobErr = fmt.Errorf("offline runtime returned no valid allocated workspace")
				}
			} else {
				workspace, jobErr = prepareWorkspace(agentConf, payload)
			}
		}
		if jobErr == nil {
			initialCheckout, jobErr = checkoutJobInputs(jobCtx, grpcClient, agentConf, payload, workspace)
		}
		if jobErr == nil {
			jobErr = validateArtifactReferences(payload.ArtifactURIs, payload.ArtifactReferences, reference, time.Now())
			if jobErr == nil {
				jobErr = handleArtifactDownload(jobCtx, payload.ArtifactURIs, payload.ArtifactReferences, workspace, st)
			}
		}
		jobErr = executeJobAttempts(ctx, jobCtx, agentConf, grpcClient, execManager, payload, artifacts, workspace, jobErr, start, session)
	}

	if session != nil {
		jobErr = errors.Join(jobErr, session.finish(ctx))
	}
	if jobCtx.Err() != nil {
		jobErr = errors.Join(jobErr, jobCtx.Err())
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
	dependencies := allJobAttemptIDs(payload)
	receipt := &executionReceipt{runtimeCompletionRequired: session != nil || runtimeCompletionRequired, runtimeBinding: runtimeBindingFromContext(ctx), jobClaim: executionClaimID(ctx, "job", payload.JobRunID), executionErr: jobErr, report: func(reportCtx context.Context) error {
		for _, stepID := range dependencies {
			if pending, ok := pendingStepReports.Load(receiptKey(ctx, "step", stepID)); ok {
				var err error
				if isTerminalStage(reportCtx) {
					err = pending.(*executionReceipt).report(reportCtx)
				} else {
					err = pending.(*executionReceipt).deliver(reportCtx, &pendingStepReports, receiptKey(ctx, "step", stepID))
				}
				if err != nil {
					var completed *completedExecutionError
					if !errors.As(err, &completed) && !terminalQuarantined(err) {
						return err
					}
				}
			}
		}
		reportCtx = context.WithValue(reportCtx, terminalDependenciesContextKey{}, dependencies)
		reportCtx = withInitialSourceCheckout(reportCtx, initialCheckout)
		return reportJobRunStatus(reportCtx, grpcClient, agentConf, payload.JobRunID, agentv1.AgentStatus_AGENT_STATUS_IDLE, int32(status), errMsg, start, end, artifacts)
	}}
	receipt.report = freezeGenerationReport(ctx, receipt.report)
	pendingJobReports.Store(receiptKey(ctx, "job", payload.JobRunID), receipt)
	return deliverJobReceipt(ctx, payload, receipt)
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
	reportCtx context.Context,
	grpcClient *grpc.ClientWrapper,
	agentConf *config.AgentConfig,
	jobRunID string,
	_ agentv1.AgentStatus,
	status int32,
	errMsg string,
	start time.Time,
	endUnix int64,
	artifacts ...*artifactExecutor,
) error {
	if (grpcClient == nil || grpcClient.AgentClient == nil) && terminalOutboxFromContext(reportCtx) == nil {
		return nil
	}
	agentID := ""
	if agentConf != nil {
		agentID = agentConf.Agent.ID
	}
	req := &agentv1.ReportJobRunStatusRequest{
		AgentId:              agentID,
		JobRunId:             jobRunID,
		Status:               status,
		ErrorMessage:         errMsg,
		StartTime:            start.Unix(),
		EndTime:              endUnix,
		RuntimeBindingDigest: runtimeBindingFromContext(reportCtx),
	}
	if generation := generationFromContext(reportCtx); generation != nil {
		req.GenerationId = generation.generation.GenerationID
		req.GenerationBindingDigest = generation.generation.BindingDigest
	}
	if len(artifacts) > 0 && artifacts[0] != nil {
		files := artifacts[0]
		req.ArtifactStorageId = files.reference.StorageID
		req.ArtifactStorageFingerprint = files.reference.Fingerprint
		if status >= int32(steprunv1.StepRunStatus_STEP_RUN_STATUS_SUCCESS) && (generationFromContext(reportCtx) == nil || status == int32(steprunv1.StepRunStatus_STEP_RUN_STATUS_SUCCESS)) {
			req.ArtifactUris = files.uploaded
			req.Artifacts = files.results
		}
	}
	if status >= int32(steprunv1.StepRunStatus_STEP_RUN_STATUS_SUCCESS) {
		req.SourceCheckout = initialSourceCheckout(reportCtx)
		return reportTerminalStatus(reportCtx, grpcClient, agentConf, "job", jobRunID, req)
	}
	ctx, cancel := grpcClient.WithTimeoutAndAuth(context.WithoutCancel(reportCtx))
	defer cancel()
	ctx = withExecutionClaim(ctx, "job", jobRunID)
	resp, err := grpcClient.AgentClient.ReportJobRunStatus(ctx, req)
	if err != nil {
		return err
	}
	if resp == nil || !resp.Success {
		return fmt.Errorf("job status report rejected")
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
	reportCtx context.Context,
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
	if (grpcClient == nil || grpcClient.AgentClient == nil || agentConf == nil) && terminalOutboxFromContext(reportCtx) == nil {
		return nil
	}
	if agentConf == nil {
		return &terminalDeliveryError{errors.New("terminal report agent is unavailable")}
	}
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
	if generation := generationFromContext(reportCtx); generation != nil {
		req.GenerationId = generation.generation.GenerationID
		req.GenerationBindingDigest = generation.generation.BindingDigest
		pure, _ := reportCtx.Value(generationPureExitKey{}).(bool)
		req.RetryableShellExit = pure
	}
	if attempt := stepAttemptFromContext(reportCtx); attempt != nil {
		if attempt.admission.Scope.StepRunID != stepRunID {
			return terminalPersistenceFailure(fmt.Errorf("step report changed its admitted attempt"))
		}
		req.StepAttemptAdmissionDigest = attempt.admission.BindingDigest
	}
	if status >= steprunv1.StepRunStatus_STEP_RUN_STATUS_SUCCESS {
		return reportTerminalStatus(reportCtx, grpcClient, agentConf, "step", stepRunID, req)
	}
	ctx, cancel := grpcClient.WithTimeoutAndAuth(context.WithoutCancel(reportCtx))
	defer cancel()
	ctx = withExecutionClaim(ctx, "step", stepRunID)
	resp, err := grpcClient.AgentClient.ReportStepRunStatus(ctx, req)
	if err != nil {
		return err
	}
	if resp == nil || !resp.Success {
		return fmt.Errorf("step status report rejected")
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

// retainStepReport 缓存所有步骤终态上报，避免临时 RPC 故障丢失跳过或失败结果
func retainStepReport(ctx context.Context, stepRunID string, executionErr error, report func(context.Context) error) error {
	receipt := &executionReceipt{executionErr: executionErr, report: freezeGenerationReport(ctx, report)}
	pendingStepReports.Store(receiptKey(ctx, "step", stepRunID), receipt)
	return receipt.deliver(ctx, &pendingStepReports, receiptKey(ctx, "step", stepRunID))
}

// executionReceipt 保留终态上报以避免队列重试重复执行 Shell
// 使用相同执行认领身份先持久化终态，再尝试投递
type executionReceipt struct {
	runtimeCompletionRequired bool
	runtimeBinding            string
	jobClaim                  string
	executionErr              error
	report                    func(context.Context) error
}

func (r *executionReceipt) deliver(ctx context.Context, pending *sync.Map, key string) error {
	if err := r.report(stageTerminalReports(ctx)); err != nil {
		return err
	}
	if err := r.report(ctx); err != nil {
		if terminalQuarantined(err) {
			pending.Delete(key)
		}
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
	if terminalQuarantined(err) {
		log.Warnw("execution task retained in durable terminal quarantine; report not accepted")
		return nil
	}
	if err == nil {
		return nil
	}
	var completed *completedExecutionError
	if errors.As(err, &completed) {
		return nil
	}
	var delivery *terminalDeliveryError
	if errors.As(err, &delivery) {
		return err
	}
	switch grpcstatus.Code(err) {
	case codes.FailedPrecondition, codes.PermissionDenied, codes.InvalidArgument, codes.NotFound:
		log.Warnw("discarding rejected execution task", "code", grpcstatus.Code(err).String())
		return nil
	}
	return err
}

// ValidateExecutionConfig 拒绝无法实际强制执行的隔离或安全配置
func ValidateExecutionConfig(conf *config.AgentConfig) error {
	if conf == nil {
		return fmt.Errorf("agent configuration is required")
	}
	mode := strings.ToLower(strings.TrimSpace(conf.Agent.Mode))
	if conf.Agent.Sandbox.Containerd.TerminalRerunEnabled && (mode != "sandbox" || !conf.Agent.Sandbox.Enable || !taskqueue.TerminalRerunAllowed(conf.Agent.Sandbox.Containerd.Policy)) {
		return fmt.Errorf("terminal whole-job new execution is not accepted or configured")
	}
	if mode == "sandbox" && conf.Agent.Sandbox.Enable {
		if conf.Agent.Sandbox.Runtime != "containerd" {
			return fmt.Errorf("sandbox runtime must be containerd")
		}
		if err := conf.Agent.Sandbox.Containerd.Policy.ValidateProduction(); err != nil {
			return err
		}
		if !conf.Agent.Sandbox.Containerd.Policy.Enabled {
			return fmt.Errorf("offline containerd runtime policy is disabled")
		}
	} else if mode != "baremetal" || conf.Agent.Sandbox.Enable {
		return fmt.Errorf("agent mode must explicitly select trusted baremetal or an accepted offline containerd profile")
	} else if conf.Agent.Sandbox.Containerd.Policy.Enabled {
		return fmt.Errorf("baremetal cannot enable a containerd runtime policy")
	}
	if len(conf.Agent.DeniedCommands) > 0 || len(conf.Agent.ResourceLimits) > 0 {
		return fmt.Errorf("baremetal executor cannot enforce deniedCommands or resourceLimits; use a dedicated trusted host and remove unsupported restrictions explicitly")
	}
	if conf.Agent.EnableDocker {
		return fmt.Errorf("Docker execution is not implemented by this worker")
	}
	if conf.TaskQueue.AutoCommit {
		return fmt.Errorf("agent taskQueue.autoCommit must be false to preserve durable terminal acknowledgements")
	}
	_, err := conf.Agent.ExecutionCapacity()
	return err
}

// quarantineCounts 仅返回本地隔离规模，不暴露负载或业务身份
func (o *terminalOutbox) quarantineCounts() (total, unreviewed int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, entry := range o.entries {
		if q := entry.record.Quarantine; q != nil {
			total++
			if len(q.Reviews) == 0 {
				unreviewed++
			}
		}
	}
	return total, unreviewed
}
