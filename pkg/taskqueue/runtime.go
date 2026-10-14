// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package taskqueue

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"math"
	"math/big"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
)

// ExecutionModeBaremetal 标识兼容的宿主执行模式
const ExecutionModeBaremetal = "baremetal"

// RuntimeContainerdOfflineV1 标识可信代码离线执行的逐作业版本化契约
const RuntimeContainerdOfflineV1 = "containerd-offline-v1"

// OfflineRuntimeAccepted 表示限定离线作业运行时已通过专用主机验收
// 操作者策略仍默认关闭，启用必须通过配置、身份和生命周期校验
const OfflineRuntimeAccepted = true

// RuntimeResources 保存明确的硬资源上限，不接受宿主路径或可选运行时参数
type RuntimeResources struct {
	CPU    string `json:"cpu"`
	Memory string `json:"memory"`
}

// RuntimeRequest 保存派发时冻结的运行时请求，绑定原作业与目标 Agent
// BindingDigest 由控制面计算，Agent 必须重新计算并随所有作业报告返回
type RuntimeRequest struct {
	Type          string           `json:"type"`
	Image         string           `json:"image"`
	Resources     RuntimeResources `json:"resources"`
	BindingDigest string           `json:"bindingDigest,omitempty"`
}

// UnmarshalJSON 拒绝未实现的资源及隔离参数，避免静默丢弃安全配置
func (r *RuntimeRequest) UnmarshalJSON(data []byte) error {
	type plain RuntimeRequest
	var decoded plain
	if err := (sonic.Config{DisallowUnknownFields: true}).Froze().Unmarshal(data, &decoded); err != nil {
		return err
	}
	*r = RuntimeRequest(decoded)
	return nil
}

// OfflineRuntimePolicy 保存操作者配置，不得由流水线或队列请求扩大
type OfflineRuntimePolicy struct {
	Enabled            bool     `json:"enabled" mapstructure:"enabled"`
	AllowWholeJobRetry bool     `json:"allowWholeJobRetry" mapstructure:"allowWholeJobRetry"`
	AllowedImages      []string `json:"allowedImages" mapstructure:"allowedImages"`
	MaxCPU             string   `json:"maxCpu" mapstructure:"maxCpu"`
	MaxMemory          string   `json:"maxMemory" mapstructure:"maxMemory"`
}

// Clone 返回独立策略快照
func (p OfflineRuntimePolicy) Clone() OfflineRuntimePolicy {
	p.AllowedImages = slices.Clone(p.AllowedImages)
	return p
}

// Validate 检查启用策略是否明确限制镜像及资源，零值保持禁用
func (p OfflineRuntimePolicy) Validate() error {
	if !p.Enabled {
		return nil
	}
	if len(p.AllowedImages) == 0 {
		return fmt.Errorf("offline runtime requires an operator image allowlist")
	}
	for _, image := range p.AllowedImages {
		if len(image) > 512 || !runtimeImagePattern.MatchString(image) {
			return fmt.Errorf("offline runtime allowlist requires immutable image@sha256 references")
		}
	}
	if _, err := runtimeCPU(p.MaxCPU); err != nil {
		return fmt.Errorf("operator CPU maximum: %w", err)
	}
	if _, err := runtimeMemory(p.MaxMemory); err != nil {
		return fmt.Errorf("operator memory maximum: %w", err)
	}
	return nil
}

// ValidateProduction 禁止在真实验收完成前启用生产配置
func (p OfflineRuntimePolicy) ValidateProduction() error {
	if p.Enabled && !OfflineRuntimeAccepted {
		return fmt.Errorf("offline containerd runtime is unavailable pending dedicated-host acceptance")
	}
	return p.Validate()
}

var runtimeImagePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._:/-]*@sha256:[a-f0-9]{64}$`)
var runtimeCPUPattern = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)?m?$`)
var runtimeMemoryPattern = regexp.MustCompile(`(?i)^([0-9]+)([KMGT](?:I?B?)?|B)?$`)

func runtimeCPU(value string) (*big.Rat, error) {
	if len(value) > 64 || !runtimeCPUPattern.MatchString(value) {
		return nil, fmt.Errorf("invalid CPU limit")
	}
	cores, ok := new(big.Rat).SetString(strings.TrimSuffix(value, "m"))
	if !ok {
		return nil, fmt.Errorf("invalid CPU limit")
	}
	if strings.HasSuffix(value, "m") {
		cores.Quo(cores, big.NewRat(1000, 1))
	}
	quota := new(big.Rat).Mul(cores, big.NewRat(100000, 1))
	if !quota.IsInt() || !quota.Num().IsInt64() || quota.Num().Int64() < 1000 {
		return nil, fmt.Errorf("CPU limit must produce an integer quota of at least 1000 microseconds")
	}
	return cores, nil
}
func runtimeMemory(value string) (int64, error) {
	if len(value) > 64 {
		return 0, fmt.Errorf("invalid memory limit")
	}
	match := runtimeMemoryPattern.FindStringSubmatch(value)
	if match == nil {
		return 0, fmt.Errorf("invalid memory limit")
	}
	size, err := strconv.ParseUint(match[1], 10, 63)
	if err != nil || size == 0 {
		return 0, fmt.Errorf("invalid memory limit")
	}
	mult := uint64(1)
	if match[2] != "" {
		switch strings.ToUpper(match[2][:1]) {
		case "K":
			mult = 1 << 10
		case "M":
			mult = 1 << 20
		case "G":
			mult = 1 << 30
		case "T":
			mult = 1 << 40
		}
	}
	if size > math.MaxInt64/mult {
		return 0, fmt.Errorf("memory limit overflows int64")
	}
	return int64(size * mult), nil
}

// ValidateRuntimeRequest 检查冻结的运行时请求与当前操作者策略，不改写请求
func ValidateRuntimeRequest(request *RuntimeRequest, policy OfflineRuntimePolicy) error {
	if request == nil {
		return nil
	}
	if !policy.Enabled {
		return fmt.Errorf("offline containerd runtime is disabled")
	}
	if err := policy.Validate(); err != nil {
		return err
	}
	if request.Type != RuntimeContainerdOfflineV1 {
		return fmt.Errorf("unsupported runtime type %q", request.Type)
	}
	if len(request.Image) > 512 || !runtimeImagePattern.MatchString(request.Image) || !slices.Contains(policy.AllowedImages, request.Image) {
		return fmt.Errorf("runtime image must be an operator-approved immutable image@sha256 reference")
	}
	cpu, err := runtimeCPU(request.Resources.CPU)
	if err != nil {
		return err
	}
	maxCPU, _ := runtimeCPU(policy.MaxCPU)
	if cpu.Cmp(maxCPU) > 0 {
		return fmt.Errorf("runtime CPU exceeds operator maximum")
	}
	memory, err := runtimeMemory(request.Resources.Memory)
	if err != nil {
		return err
	}
	maxMemory, _ := runtimeMemory(policy.MaxMemory)
	if memory > maxMemory {
		return fmt.Errorf("runtime memory exceeds operator maximum")
	}
	return nil
}

// RuntimeBindingDigest 将镜像、资源与原作业和 Agent 身份绑定，不包含可变标签
func RuntimeBindingDigest(pipelineID, runID, jobID, agentID string, request *RuntimeRequest) (string, error) {
	if request == nil {
		return "", nil
	}
	for _, value := range []string{pipelineID, runID, jobID, agentID} {
		if value == "" || strings.TrimSpace(value) != value {
			return "", fmt.Errorf("exact runtime dispatch identity is required")
		}
	}
	if request.Type != RuntimeContainerdOfflineV1 || len(request.Image) > 512 || !runtimeImagePattern.MatchString(request.Image) {
		return "", fmt.Errorf("invalid runtime binding")
	}
	if _, err := runtimeCPU(request.Resources.CPU); err != nil {
		return "", err
	}
	if _, err := runtimeMemory(request.Resources.Memory); err != nil {
		return "", err
	}
	digest := sha256.New()
	for _, value := range []string{"arcentra-runtime-v1", pipelineID, runID, jobID, agentID, request.Type, request.Image, request.Resources.CPU, request.Resources.Memory} {
		_, _ = digest.Write(binary.BigEndian.AppendUint64(nil, uint64(len(value))))
		_, _ = digest.Write([]byte(value))
	}
	return "v1:" + hex.EncodeToString(digest.Sum(nil)), nil
}

// ValidateJobRuntime 在任何宿主暂存或命令执行前检查离线作业的完整契约
func ValidateJobRuntime(payload *JobRunTaskPayload, policy OfflineRuntimePolicy) error {
	if payload == nil {
		return fmt.Errorf("job payload is required")
	}
	if payload.Runtime == nil {
		return nil
	}
	if err := ValidateRuntimeRequest(payload.Runtime, policy); err != nil {
		return err
	}
	if err := ValidateRuntimeRetry(payload.Retry, policy); err != nil {
		return err
	}
	if err := ValidateJobRetryPlan(payload); err != nil {
		return err
	}
	if _, exists := payload.Env[RuntimeGenerationEnv]; exists {
		return fmt.Errorf("offline runtime reserves its generation environment variable")
	}
	for _, step := range payload.Steps {
		if _, exists := step.Env[RuntimeGenerationEnv]; exists {
			return fmt.Errorf("offline runtime reserves its generation environment variable")
		}
	}
	if payload.Workspace != "" {
		return fmt.Errorf("runtime job cannot override the host workspace")
	}
	if payload.Source != nil {
		if payload.Source.Type != "git" {
			return fmt.Errorf("runtime source requires explicit Git staging")
		}
		parsed, err := url.Parse(payload.Source.Repo)
		if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http" && parsed.Scheme != "ssh") || parsed.Host == "" {
			return fmt.Errorf("runtime source cannot use host paths or local transport")
		}
	}
	attempts := 1
	if payload.Retry != nil {
		attempts = int(payload.Retry.MaxAttempts)
	}
	if err := validateRuntimeSteps(payload.Steps, attempts); err != nil {
		return err
	}
	digest, err := RuntimeBindingDigest(payload.PipelineID, payload.PipelineRunID, payload.JobRunID, payload.AgentID, payload.Runtime)
	if err != nil {
		return err
	}
	if payload.Runtime.BindingDigest != digest {
		return fmt.Errorf("runtime request does not match dispatch binding")
	}
	return nil
}

// ValidateStandaloneRuntime 拒绝为单步队列任务隐式创建或回退运行时
func ValidateStandaloneRuntime(payload *StepRunTaskPayload) error {
	if payload != nil && payload.Runtime != nil {
		return fmt.Errorf("offline runtime requires a complete job; standalone steps are unsupported")
	}
	return nil
}

// ValidateRuntimeSteps 限定下载前缀、Shell 主体和上传后缀，不允许交错或隐式宿主执行
func ValidateRuntimeSteps(steps []StepPayload) error { return validateRuntimeSteps(steps, 1) }

func validateRuntimeSteps(steps []StepPayload, attempts int) error {
	phase, shells := 0, 0
	for _, step := range steps {
		if len(step.AttemptRunIDs) > attempts {
			return fmt.Errorf("offline runtime does not support retry attempts")
		}
		switch step.Uses {
		case "shell", "builtin:shell":
			if phase == 2 {
				return fmt.Errorf("shell steps cannot follow artifact upload")
			}
			phase = 1
			shells++
			if step.Action != "" && step.Action != "run" {
				return fmt.Errorf("offline runtime accepts shell action run or empty only")
			}
			for key := range step.Args {
				switch key {
				case "run", "script", "command", "commands":
				default:
					return fmt.Errorf("unsupported runtime shell argument %q", key)
				}
			}
		case "builtin:artifacts":
			if step.ContinueOnError {
				return fmt.Errorf("offline artifact staging cannot continue on error")
			}
			if err := validateRuntimeArtifactArgs(step); err != nil {
				return err
			}
			switch step.Action {
			case "download":
				if phase != 0 {
					return fmt.Errorf("artifact downloads must precede every shell step")
				}
			case "upload":
				if shells == 0 {
					return fmt.Errorf("artifact uploads must follow shell steps")
				}
				phase = 2
			default:
				return fmt.Errorf("unsupported artifact staging action")
			}
		default:
			return fmt.Errorf("offline runtime supports shell and artifact staging only")
		}
	}
	if shells == 0 {
		return fmt.Errorf("offline runtime job requires a shell step")
	}
	return nil
}

func validateRuntimeArtifactArgs(step StepPayload) error {
	for key, value := range step.Args {
		switch key {
		case "path":
			name, ok := value.(string)
			if !ok {
				return fmt.Errorf("artifact path must be a string")
			}
			if err := runtimeArtifactPath(name); err != nil {
				return err
			}
		case "paths":
			switch names := value.(type) {
			case []string:
				for _, name := range names {
					if err := runtimeArtifactPath(name); err != nil {
						return err
					}
				}
			case []any:
				for _, item := range names {
					name, ok := item.(string)
					if !ok {
						return fmt.Errorf("artifact paths must contain strings")
					}
					if err := runtimeArtifactPath(name); err != nil {
						return err
					}
				}
			default:
				return fmt.Errorf("artifact paths must be a list")
			}
		case "job":
			if step.Action != "download" {
				return fmt.Errorf("artifact job applies only to download")
			}
			if _, ok := value.(string); !ok {
				return fmt.Errorf("artifact job must be a string")
			}
		case "expireIn":
			if step.Action != "upload" {
				return fmt.Errorf("artifact expiry applies only to upload")
			}
			if _, ok := value.(string); !ok {
				return fmt.Errorf("artifact expiry must be a string")
			}
		default:
			return fmt.Errorf("unsupported runtime artifact argument %q", key)
		}
	}
	return nil
}
func runtimeArtifactPath(name string) error {
	if name == "" || path.IsAbs(name) || path.Clean(name) != name || strings.ContainsAny(name, "\\:\x00") {
		return fmt.Errorf("runtime artifacts require clean relative paths")
	}
	for _, component := range strings.Split(name, "/") {
		if component == ".." {
			return fmt.Errorf("runtime artifact path escapes workspace")
		}
	}
	return nil
}

// RuntimeGenerationEnv 是受信任工作器设置的轮次变量，用户配置不得覆盖
const RuntimeGenerationEnv = "ARCENTRA_JOB_GENERATION"

// ValidateRuntimeRetry 校验默认关闭的整作业新会话重试策略
func ValidateRuntimeRetry(retry *pipelinev1.Retry, policy OfflineRuntimePolicy) error {
	if retry == nil {
		return nil
	}
	if !policy.AllowWholeJobRetry {
		return fmt.Errorf("offline runtime retries are unsupported unless operator allowWholeJobRetry is enabled")
	}
	if retry.Mode != "whole_job" {
		return fmt.Errorf("offline runtime supports whole_job retry only; failed_step requires a proven quiescence boundary")
	}
	if !retry.Idempotent || retry.MaxAttempts < 2 || retry.MaxAttempts > 5 {
		return fmt.Errorf("offline whole_job retry requires idempotent=true and maxAttempts between 2 and 5")
	}
	if len(retry.AllowedExitCodes) == 0 {
		return fmt.Errorf("offline retry requires explicit allowedExitCodes")
	}
	seen := map[int32]bool{}
	for _, code := range retry.AllowedExitCodes {
		if code < 1 || code > 125 || seen[code] {
			return fmt.Errorf("offline retry requires distinct allowed exit codes between 1 and 125")
		}
		seen[code] = true
	}
	if retry.Delay != "" {
		delay, err := time.ParseDuration(retry.Delay)
		if err != nil || delay < 0 || delay > time.Hour {
			return fmt.Errorf("offline retry delay must be between 0s and 1h")
		}
	}
	return nil
}
