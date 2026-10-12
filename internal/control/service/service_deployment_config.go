// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package service

import (
	"context"
	"errors"
	"strings"

	cluster "github.com/arcentrix/arcentra/internal/shared/deployment"
	artifactutil "github.com/arcentrix/arcentra/pkg/artifact"
)

// ConfigureDeployment 在启动阶段装载精确目标白名单，必须在启动 HTTP 服务之前调用
// 关闭配置不读取文件或访问网络；失败时清空注册表并返回错误，禁止部分启用
func ConfigureDeployment(ctx context.Context, s *DeploymentService, config cluster.Config) error {
	if s == nil {
		return cluster.ErrUnconfigured
	}
	s.targets = DeploymentTargets{}
	if !config.Enabled {
		return nil
	}
	if len(config.Targets) == 0 || len(config.Targets) > 100 {
		return errors.New("deployment configuration requires 1 to 100 exact targets")
	}
	bindings := DeploymentTargets{}
	workloads := map[string]bool{}
	for _, target := range config.Targets {
		if target.Tier == "" {
			target.Tier = "prod"
		}
		if target.Tier != "dev" && target.Tier != "staging" && target.Tier != "prod" {
			return errors.New("invalid deployment authorization tier")
		}
		if artifactutil.Identity(target.Ref) != nil || artifactutil.Identity(target.ProjectID) != nil {
			return errors.New("invalid deployment target reference or project")
		}
		key := strings.TrimRight(target.Server, "/") + "\x00" + target.Namespace + "\x00" + target.Deployment
		if _, exists := bindings[target.Ref]; exists || workloads[key] {
			return errors.New("duplicate deployment target or workload binding")
		}
		if err := cluster.ValidatePipelineApprovalPolicy(target.PipelineApproval); err != nil {
			return err
		}
		adapter, err := cluster.NewConfiguredAdapter(ctx, target)
		if err != nil {
			return err
		}
		binding := DeploymentTargetBinding{PipelineApproval: cluster.ClonePipelineApprovalPolicy(target.PipelineApproval), AllowPipelineDeployments: target.AllowPipelineDeployments, ProjectID: target.ProjectID, Tier: target.Tier, Target: cluster.Target{Namespace: target.Namespace, Deployment: target.Deployment, Container: target.Container}, Adapter: adapter}
		binding.Fingerprint = deploymentTargetFingerprint(target.Ref, binding, target.Server)
		bindings[target.Ref] = binding
		workloads[key] = true
	}
	s.targets = bindings
	return nil
}
