package deployment

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/bytedance/sonic"
)

// PipelineArtifactReference 指定本次运行内的准确制品来源
type PipelineArtifactReference struct {
	Job  string `json:"job"`
	Step string `json:"step"`
	Name string `json:"name"`
}

// PipelinePlanStep 固定前置作业的逻辑步骤及位置
type PipelinePlanStep struct {
	Name  string `json:"name"`
	Index int    `json:"index"`
}

// PipelinePredecessor 固定必须成功完成的前置作业
type PipelinePredecessor struct {
	JobName string             `json:"jobName"`
	Steps   []PipelinePlanStep `json:"steps"`
}

// PipelineDeploymentPlan 保存经验证的不可变部署计划
type PipelineDeploymentPlan struct {
	Version          int                       `json:"version"`
	ApprovalRequired bool                      `json:"approvalRequired,omitempty"`
	JobName          string                    `json:"jobName"`
	StepName         string                    `json:"stepName"`
	EnvironmentID    string                    `json:"environmentId"`
	Artifact         PipelineArtifactReference `json:"artifact"`
	TimeoutSeconds   int64                     `json:"timeoutSeconds"`
	Predecessors     []PipelinePredecessor     `json:"predecessors"`
}

var planLiteral = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.-]{0,127}$`)

// DecodePipelinePlan 严格解析唯一的最终控制面部署作业，无部署时返回 nil
func DecodePipelinePlan(spec *pipelinev1.Spec) (*PipelineDeploymentPlan, error) {
	if spec == nil {
		return nil, fmt.Errorf("pipeline is required")
	}
	var deployJob *pipelinev1.Job
	var deployStep *pipelinev1.Step
	jobs := map[string]*pipelinev1.Job{}
	for _, job := range spec.Jobs {
		if job == nil {
			return nil, fmt.Errorf("nil job")
		}
		if _, found := jobs[job.Name]; found {
			return nil, fmt.Errorf("duplicate job %q", job.Name)
		}
		jobs[job.Name] = job
		for _, step := range job.Steps {
			if step == nil {
				return nil, fmt.Errorf("nil step")
			}
			if step.Uses == "builtin:kubernetes" {
				if deployJob != nil {
					return nil, fmt.Errorf("only one kubernetes deployment step is supported")
				}
				deployJob, deployStep = job, step
			}
		}
	}
	if deployJob == nil {
		return nil, nil
	}
	for _, trigger := range spec.Triggers {
		if trigger == nil || trigger.Type != "manual" {
			return nil, fmt.Errorf("deployment-bearing pipelines support verified manual triggers only")
		}
	}
	job, step := deployJob, deployStep
	for _, other := range spec.Jobs {
		if other.Approval != nil && (other != deployJob || !IsPipelineDeploymentApproval(other.Approval)) {
			return nil, fmt.Errorf("job %q: only required manual builtin:deployment approval with empty params on the final deployment job is supported", other.Name)
		}
		if other.Target != nil {
			return nil, fmt.Errorf("legacy target blocks cannot coexist with pipeline deployments")
		}
	}
	if step.Action != "deploy" || len(job.Steps) != 1 {
		return nil, fmt.Errorf("kubernetes deploy must be the only step in its job")
	}
	if job.Retry != nil || job.Source != nil || job.Target != nil || job.When != "" || job.Concurrency != "" || len(job.Env) > 0 || job.Notify != nil || len(job.Triggers) > 0 {
		return nil, fmt.Errorf("deployment job cannot use retry, source, target, conditions, concurrency, environment, notifications, or job triggers")
	}
	if step.ContinueOnError || step.RunOnAgent || step.AgentSelector != nil || step.When != "" || len(step.Env) > 0 {
		return nil, fmt.Errorf("deployment step must be unconditional, controller-only, and cannot continue on error")
	}
	timeout := step.Timeout
	if timeout == "" {
		timeout = job.Timeout
	}
	duration, err := time.ParseDuration(timeout)
	if err != nil || duration < time.Second || duration > time.Hour || duration%time.Second != 0 {
		return nil, fmt.Errorf("deployment timeout must be an explicit whole number of seconds between 1s and 1h")
	}
	if job.Timeout != "" {
		d, e := time.ParseDuration(job.Timeout)
		if e != nil || d < duration || d > time.Hour {
			return nil, fmt.Errorf("deployment job timeout must cover the step timeout and be at most 1h")
		}
	}
	args := step.GetArgs().AsMap()
	if len(args) != 2 {
		return nil, fmt.Errorf("deployment args must contain exactly environmentId and artifact")
	}
	environment, ok := args["environmentId"].(string)
	if !ok || !planLiteral.MatchString(environment) {
		return nil, fmt.Errorf("environmentId must be a literal identifier")
	}
	artifact, ok := args["artifact"].(map[string]any)
	if !ok || len(artifact) != 3 {
		return nil, fmt.Errorf("artifact must contain exactly job, step, and name")
	}
	ref := PipelineArtifactReference{}
	for k, dst := range map[string]*string{"job": &ref.Job, "step": &ref.Step, "name": &ref.Name} {
		value, ok := artifact[k].(string)
		if !ok || !planLiteral.MatchString(value) {
			return nil, fmt.Errorf("artifact %s must be a literal identifier", k)
		}
		*dst = value
	}
	producer := jobs[ref.Job]
	if producer == nil || producer == job {
		return nil, fmt.Errorf("artifact producer must be another job in this run")
	}
	explicit := false
	for _, name := range job.DependsOn {
		explicit = explicit || name == ref.Job
	}
	if !explicit {
		return nil, fmt.Errorf("deployment must explicitly depend on its artifact producer")
	}
	upload := false
	for _, s := range producer.Steps {
		if s.Name == ref.Step {
			upload = s.Uses == "builtin:artifacts" && strings.EqualFold(s.Action, "upload") && !s.ContinueOnError
		}
	}
	if !upload {
		return nil, fmt.Errorf("artifact producer step must be an explicit artifacts upload without continueOnError")
	}
	visiting, visited := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(name string) error {
		if visiting[name] {
			return fmt.Errorf("dependency cycle at %q", name)
		}
		if visited[name] {
			return nil
		}
		j := jobs[name]
		if j == nil {
			return fmt.Errorf("unknown dependency %q", name)
		}
		visiting[name] = true
		for _, dependency := range j.DependsOn {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		visiting[name] = false
		visited[name] = true
		return nil
	}
	if err := visit(job.Name); err != nil {
		return nil, err
	}
	if len(visited) != len(jobs) {
		return nil, fmt.Errorf("deployment job must be the final DAG job with every other job in its predecessor closure")
	}
	result := &PipelineDeploymentPlan{Version: 1, ApprovalRequired: job.Approval != nil, JobName: job.Name, StepName: step.Name, EnvironmentID: environment, Artifact: ref, TimeoutSeconds: int64(duration / time.Second)}
	for _, j := range spec.Jobs {
		if j == job {
			continue
		}
		predecessor := PipelinePredecessor{JobName: j.Name, Steps: make([]PipelinePlanStep, 0, len(j.Steps))}
		for i, s := range j.Steps {
			predecessor.Steps = append(predecessor.Steps, PipelinePlanStep{Name: s.Name, Index: i})
		}
		result.Predecessors = append(result.Predecessors, predecessor)
	}
	sort.Slice(result.Predecessors, func(i, j int) bool { return result.Predecessors[i].JobName < result.Predecessors[j].JobName })
	return result, nil
}

// Canonical 返回稳定的计划 JSON 及其 SHA-256 摘要
func (p *PipelineDeploymentPlan) Canonical() (string, string, error) {
	if p == nil {
		return "", "", fmt.Errorf("deployment plan is required")
	}
	data, err := sonic.MarshalString(p)
	if err != nil {
		return "", "", err
	}
	return data, fmt.Sprintf("%x", sha256.Sum256([]byte(data))), nil
}

// ParsePipelinePlan 校验持久化部署计划的完整性及规范编码
func ParsePipelinePlan(data, hash string) (*PipelineDeploymentPlan, error) {
	if data == "" || hash == "" || fmt.Sprintf("%x", sha256.Sum256([]byte(data))) != hash {
		return nil, fmt.Errorf("deployment plan integrity mismatch")
	}
	var p PipelineDeploymentPlan
	if err := sonic.UnmarshalString(data, &p); err != nil {
		return nil, err
	}
	canonical, digest, err := p.Canonical()
	if err != nil || canonical != data || digest != hash || p.Version != 1 || p.TimeoutSeconds < 1 || p.TimeoutSeconds > 3600 || !planLiteral.MatchString(p.EnvironmentID) || !planLiteral.MatchString(p.JobName) || !planLiteral.MatchString(p.StepName) || !planLiteral.MatchString(p.Artifact.Job) || !planLiteral.MatchString(p.Artifact.Step) || !planLiteral.MatchString(p.Artifact.Name) || len(p.Predecessors) == 0 {
		return nil, fmt.Errorf("invalid immutable deployment plan")
	}
	return &p, nil
}

// IsPipelineDeploymentApproval 仅识别控制面持久化的精确人工部署门禁
func IsPipelineDeploymentApproval(approval *pipelinev1.Approval) bool {
	return approval != nil && approval.Required && approval.Type == "manual" && approval.Plugin == "builtin:deployment" && len(approval.GetParams().GetFields()) == 0
}
