// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package taskqueue

import (
	"bytes"
	"context"
	"errors"
	"net/url"
	"regexp"
	"sort"
	"strings"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/agent/config"
	agentgrpc "github.com/arcentrix/arcentra/internal/shared/grpc"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"google.golang.org/protobuf/proto"
)

type terminalOutboxContextKey struct{}
type terminalStageContextKey struct{}
type terminalDependenciesContextKey struct{}
type terminalSecretsContextKey struct{}

func withTerminalOutbox(ctx context.Context, outbox *terminalOutbox) context.Context {
	return context.WithValue(ctx, terminalOutboxContextKey{}, outbox)
}
func terminalOutboxFromContext(ctx context.Context) *terminalOutbox {
	outbox, _ := ctx.Value(terminalOutboxContextKey{}).(*terminalOutbox)
	return outbox
}
func stageTerminalReports(ctx context.Context) context.Context {
	return context.WithValue(ctx, terminalStageContextKey{}, true)
}
func isTerminalStage(ctx context.Context) bool {
	staging, _ := ctx.Value(terminalStageContextKey{}).(bool)
	return staging
}
func receiptKey(ctx context.Context, kind, runID string) string {
	if outbox := terminalOutboxFromContext(ctx); outbox != nil {
		return outbox.binding + "/" + kind + "/" + runID
	}
	return runID
}

// terminalDeliveryError 即使控制面拒绝终态，也必须保留记录并阻止消费确认
// 日志不输出远端提供的消息；Unwrap 保留状态码供重试和诊断判断
type terminalDeliveryError struct{ cause error }

func (e *terminalDeliveryError) Error() string { return "terminal report delivery failed" }
func (e *terminalDeliveryError) Unwrap() error { return e.cause }

// terminalPersistenceError 标记无法可靠保存或删除收据的错误，必须阻止后续命令
type terminalPersistenceError struct{ cause error }

func (e *terminalPersistenceError) Error() string { return "terminal report persistence failed" }
func (e *terminalPersistenceError) Unwrap() error { return e.cause }
func terminalPersistenceFailed(err error) bool {
	var persistence *terminalPersistenceError
	return errors.As(err, &persistence)
}
func terminalPersistenceFailure(err error) error {
	return &terminalDeliveryError{&terminalPersistenceError{err}}
}

func reportTerminalStatus(ctx context.Context, client *agentgrpc.ClientWrapper, conf *config.AgentConfig, kind, runID string, message proto.Message) error {
	// 任务取消只停止执行；终态落盘及有限时 RPC 仍需完成
	ctx = context.WithoutCancel(ctx)
	outbox := terminalOutboxFromContext(ctx)
	if outbox != nil && (conf == nil || terminalBinding(conf.Agent.ID, conf.Grpc.ServerAddr) != outbox.binding) {
		return terminalPersistenceFailure(errors.New("terminal report identity changed during execution"))
	}
	payload, err := encodeSafeTerminalPayload(ctx, message)
	if err != nil {
		return terminalPersistenceFailure(err)
	}
	deps, _ := ctx.Value(terminalDependenciesContextKey{}).([]string)
	record := terminalRecord{Kind: kind, RunID: runID, ClaimID: executionClaimID(ctx, kind, runID), Payload: payload}
	if kind == "job" {
		record.Dependencies = append([]string(nil), deps...)
	}
	if outbox != nil {
		if err := outbox.put(ctx, record); err != nil {
			return terminalPersistenceFailure(err)
		}
	}
	if isTerminalStage(ctx) {
		return nil
	}
	if outbox != nil {
		if _, err := outbox.deliver(ctx, client, kind, runID); err != nil {
			var storageError *terminalIOError
			if errors.As(err, &storageError) || errors.Is(err, errTerminalOutboxClosed) {
				return terminalPersistenceFailure(err)
			}
			return &terminalDeliveryError{err}
		}
	} else if client != nil && client.AgentClient != nil {
		if err := sendTerminalRecord(ctx, client, record); err != nil {
			return &terminalDeliveryError{err}
		}
	}
	completeExecutionClaim(ctx, kind, runID)
	return nil
}

func replayTerminalForTask(ctx context.Context, client *agentgrpc.ClientWrapper, conf *config.AgentConfig, kind, runID string) (bool, error) {
	outbox := terminalOutboxFromContext(ctx)
	if outbox == nil {
		return false, nil
	}
	if conf == nil || terminalBinding(conf.Agent.ID, conf.Grpc.ServerAddr) != outbox.binding {
		return true, &terminalDeliveryError{errors.New("terminal report identity mismatch")}
	}
	found, err := outbox.deliver(context.WithoutCancel(ctx), client, kind, runID)
	if err != nil {
		return found, &terminalDeliveryError{err}
	}
	return found, nil
}

var terminalCredentialURL = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^\s/@]+(?::[^\s/@]*)?@`)
var terminalCredentialQuery = regexp.MustCompile(`(?i)([?&](?:access_token|token|password|signature|x-amz-signature|x-goog-signature)=)[^&\s]+`)

func withTerminalSecrets(ctx context.Context, conf *config.AgentConfig, job *taskpayload.JobRunTaskPayload, step *taskpayload.StepRunTaskPayload) context.Context {
	previous, _ := ctx.Value(terminalSecretsContextKey{}).([]string)
	secrets := append([]string(nil), previous...)
	addEnv := func(env map[string]string) {
		for key, value := range env {
			if strings.HasPrefix(key, "secrets.") && value != "" {
				secrets = append(secrets, value)
			}
		}
	}
	if conf != nil {
		secrets = append(secrets, conf.Grpc.Token, conf.Grpc.RegistrationToken, conf.MessageQueue.Kafka.Sasl.Password, conf.MessageQueue.Kafka.Ssl.Password)
		addEnv(conf.Agent.EnvVars)
	}
	if job != nil {
		addEnv(job.Env)
		if job.Source != nil {
			secrets = append(secrets, job.Source.Auth["password"], job.Source.Auth["token"])
		}
		for _, item := range job.Steps {
			addEnv(item.Env)
		}
	}
	if step != nil {
		addEnv(step.Env)
	}
	unique := make(map[string]struct{})
	for _, secret := range secrets {
		if secret != "" {
			unique[secret] = struct{}{}
			unique[url.QueryEscape(secret)] = struct{}{}
			unique[url.PathEscape(secret)] = struct{}{}
		}
	}
	secrets = make([]string, 0, len(unique))
	for secret := range unique {
		secrets = append(secrets, secret)
	}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	return context.WithValue(ctx, terminalSecretsContextKey{}, secrets)
}

func encodeSafeTerminalPayload(ctx context.Context, message proto.Message) ([]byte, error) {
	// 对执行器之外产生的错误再脱敏，任何持久化写入前完成
	secrets, _ := ctx.Value(terminalSecretsContextKey{}).([]string)
	pairs := make([]string, 0, len(secrets)*2)
	for _, secret := range secrets {
		pairs = append(pairs, secret, "[REDACTED]")
	}
	replacer := strings.NewReplacer(pairs...)
	redact := func(value string) string {
		value = replacer.Replace(value)
		value = terminalCredentialURL.ReplaceAllString(value, "${1}[REDACTED]@")
		return terminalCredentialQuery.ReplaceAllString(value, "${1}[REDACTED]")
	}
	copied := proto.Clone(message)
	switch req := copied.(type) {
	case *agentv1.ReportJobRunStatusRequest:
		req.ErrorMessage = redact(req.ErrorMessage)
	case *agentv1.ReportStepRunStatusRequest:
		req.ErrorMessage = redact(req.ErrorMessage)
		for key, value := range req.Metrics {
			req.Metrics[key] = redact(value)
		}
	default:
		return nil, errors.New("unsupported terminal report payload")
	}
	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(copied)
	if err != nil {
		return nil, errors.New("cannot encode terminal report payload")
	}
	// 运行标识和产物元数据不能被替换；发现凭据时拒绝持久化和确认
	for _, secret := range secrets {
		if bytes.Contains(data, []byte(secret)) {
			return nil, errors.New("terminal report metadata contains a declared secret")
		}
	}
	return data, nil
}

func (o *terminalOutbox) replayBound(ctx context.Context, client *agentgrpc.ClientWrapper, conf *config.AgentConfig) error {
	if conf == nil || terminalBinding(conf.Agent.ID, conf.Grpc.ServerAddr) != o.binding {
		return errors.New("terminal report identity changed; restart with the original agent and controller")
	}
	return o.replay(ctx, client)
}
