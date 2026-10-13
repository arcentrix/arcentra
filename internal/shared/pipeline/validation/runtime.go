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
	if len(pipeline.Jobs) != 1 {
		return nil, fmt.Errorf("offline runtime currently requires exactly one job")
	}
	job := pipeline.Jobs[0]
	if job == nil {
		return nil, fmt.Errorf("runtime job is nil")
	}
	if job.Target != nil {
		return nil, fmt.Errorf("offline runtime does not support deployment targets")
	}
	if job.Retry != nil {
		return nil, fmt.Errorf("offline runtime retries are unsupported; start a fresh run")
	}
	if job.Source != nil {
		parsed, err := url.Parse(job.Source.Repo)
		if job.Source.Type != "git" || err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https" && parsed.Scheme != "ssh") || parsed.Host == "" {
			return nil, fmt.Errorf("offline source requires a remote Git URL; host paths are unsupported")
		}
	}
	steps := make([]taskqueue.StepPayload, 0, len(job.Steps))
	for _, step := range job.Steps {
		if step == nil {
			return nil, fmt.Errorf("runtime step is nil")
		}
		if !step.RunOnAgent {
			return nil, fmt.Errorf("offline runtime requires every step to run on the selected Agent")
		}
		steps = append(steps, taskqueue.StepPayload{Uses: step.Uses, Action: step.Action, Args: spec.StructAsMap(step.Args)})
	}
	if err := taskqueue.ValidateRuntimeSteps(steps); err != nil {
		return nil, err
	}
	return request, nil
}
