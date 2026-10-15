// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package validation

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
)

// NewSchemaValidatorWithRuntimePolicy 显式注入隔离测试或已验收启动路径的操作者策略
// 普通构造器保持禁用；调用方必须先通过生产验收开关，不能使用流水线提供策略
func NewSchemaValidatorWithRuntimePolicy(policy taskqueue.OfflineRuntimePolicy) *SchemaValidator {
	return &SchemaValidator{runtimePolicy: policy.Clone()}
}

// ResolveRuntime 验证并复制运行时请求；本地兼容模式返回 nil
func ResolveRuntime(pipeline *spec.Pipeline, policy taskqueue.OfflineRuntimePolicy) (*taskqueue.RuntimeRequest, error) {
	if pipeline == nil {
		return nil, fmt.Errorf("pipeline is nil")
	}
	runtime := pipeline.Runtime
	if runtime == nil {
		return nil, nil
	}
	switch strings.ToLower(strings.TrimSpace(runtime.Type)) {
	case "", "local", "baremetal":
		if strings.TrimSpace(runtime.Image) != "" || runtime.Resources != nil || len(spec.StructAsMap(runtime.Config)) > 0 {
			return nil, fmt.Errorf("runtime image, resources, and isolation configuration are unsupported for local execution")
		}
		return nil, nil
	}
	if runtime.Type != taskqueue.RuntimeContainerdOfflineV1 {
		return nil, fmt.Errorf("runtime type %q is unsupported", runtime.Type)
	}
	if _, exists := runtime.Env[taskqueue.RuntimeGenerationEnv]; exists {
		return nil, fmt.Errorf("offline runtime reserves its generation environment variable")
	}
	if _, exists := pipeline.Variables[taskqueue.RuntimeGenerationEnv]; exists {
		return nil, fmt.Errorf("offline runtime reserves its generation variable")
	}
	if len(spec.StructAsMap(runtime.Config)) > 0 {
		return nil, fmt.Errorf("offline runtime does not accept isolation flags, mounts, host paths, or network configuration")
	}
	if runtime.Resources == nil || runtime.Resources.CpuRequest != "" || runtime.Resources.MemoryRequest != "" {
		return nil, fmt.Errorf("offline runtime requires CPU and memory limits only; requests are unsupported")
	}
	request := &taskqueue.RuntimeRequest{Type: runtime.Type, Image: runtime.Image, Resources: taskqueue.RuntimeResources{CPU: runtime.Resources.CpuLimit, Memory: runtime.Resources.MemoryLimit}}
	if err := taskqueue.ValidateRuntimeRequest(request, policy); err != nil {
		return nil, err
	}
	if err := validateOfflineRuntimeChain(pipeline.Jobs); err != nil {
		return nil, err
	}
	for _, job := range pipeline.Jobs {
		if err := validateOfflineRuntimeJob(job, policy); err != nil {
			return nil, fmt.Errorf("offline runtime job %q: %w", job.Name, err)
		}
	}
	return request, nil
}

// validateOfflineRuntimeChain 仅接受一个根和显式的线性依赖，不依赖 jobs 的声明顺序
func validateOfflineRuntimeChain(jobs []*spec.Job) error {
	if len(jobs) == 0 {
		return fmt.Errorf("offline runtime requires at least one job")
	}
	byName := make(map[string]struct{}, len(jobs))
	for i, job := range jobs {
		if job == nil {
			return fmt.Errorf("offline runtime job[%d] is nil", i)
		}
		if strings.TrimSpace(job.Name) == "" {
			return fmt.Errorf("offline runtime job[%d] requires a name", i)
		}
		if _, exists := byName[job.Name]; exists {
			return fmt.Errorf("offline runtime has duplicate job name %q", job.Name)
		}
		byName[job.Name] = struct{}{}
	}

	successor := make(map[string]string, len(jobs))
	root, roots := "", 0
	for _, job := range jobs {
		switch len(job.DependsOn) {
		case 0:
			root = job.Name
			roots++
		case 1:
			dependency := job.DependsOn[0]
			if _, exists := byName[dependency]; !exists {
				return fmt.Errorf("offline runtime job %q depends on unknown job %q", job.Name, dependency)
			}
			if _, exists := successor[dependency]; exists {
				return fmt.Errorf("offline runtime requires a linear dependency chain; job %q has multiple dependents", dependency)
			}
			successor[dependency] = job.Name
		default:
			return fmt.Errorf("offline runtime job %q must have exactly one explicit predecessor", job.Name)
		}
	}
	if roots != 1 {
		return fmt.Errorf("offline runtime requires exactly one root job; every later job must explicitly depend on its predecessor")
	}

	// 单入单出仍可能包含与根隔离的环；从唯一根遍历必须覆盖所有节点
	visited := 0
	for name := root; name != ""; name = successor[name] {
		visited++
		if visited > len(jobs) {
			return fmt.Errorf("offline runtime dependency chain contains a cycle")
		}
	}
	if visited != len(jobs) {
		return fmt.Errorf("offline runtime requires every job in one acyclic dependency chain")
	}
	return nil
}

func validateOfflineRuntimeJob(job *spec.Job, policy taskqueue.OfflineRuntimePolicy) error {
	if job.Target != nil {
		return fmt.Errorf("offline runtime does not support deployment targets")
	}
	if err := taskqueue.ValidateRuntimeRetry(job.Retry, policy); err != nil {
		return err
	}
	if job.Retry != nil && job.Approval != nil && !IsDurableRetryApproval(job, policy) {
		return fmt.Errorf("offline retry with approval is unsupported until durable approval binds the complete retry plan")
	}
	if _, exists := job.Env[taskqueue.RuntimeGenerationEnv]; exists {
		return fmt.Errorf("offline runtime reserves its generation environment variable")
	}
	if job.Source != nil {
		parsed, err := url.Parse(job.Source.Repo)
		if job.Source.Type != "git" || err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https" && parsed.Scheme != "ssh") || parsed.Host == "" {
			return fmt.Errorf("offline source requires a remote Git URL; host paths are unsupported")
		}
	}
	steps := make([]taskqueue.StepPayload, 0, len(job.Steps))
	for _, step := range job.Steps {
		if step == nil {
			return fmt.Errorf("runtime step is nil")
		}
		if job.Retry != nil && job.Retry.Mode == "failed_step" && step.ContinueOnError {
			return fmt.Errorf("offline failed_step retry does not support continueOnError; a failed boundary must end or retry the same step")
		}
		if _, exists := step.Env[taskqueue.RuntimeGenerationEnv]; exists {
			return fmt.Errorf("offline runtime reserves its generation environment variable")
		}
		if !step.RunOnAgent {
			return fmt.Errorf("offline runtime requires every step to run on the selected Agent")
		}
		steps = append(steps, taskqueue.StepPayload{Uses: step.Uses, Action: step.Action, Args: spec.StructAsMap(step.Args), ContinueOnError: step.ContinueOnError})
	}
	if err := taskqueue.ValidateRuntimeSteps(steps); err != nil {
		return err
	}
	return nil
}
