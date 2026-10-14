// Copyright 2026 Arcentra Authors.
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
	"fmt"
	"strings"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	artifactutil "github.com/arcentrix/arcentra/pkg/artifact"
)

const (
	// TaskTypeJobRun identifies job-run execution tasks.
	// A job is the minimum schedulable unit dispatched to an Agent.
	TaskTypeJobRun = "pipeline.job_run"
)

// JobRunTaskPayload is the Kafka payload for dispatching a complete job to an Agent.
// The Agent receives the entire job definition and executes all steps sequentially.
type JobRunTaskPayload struct {
	Runtime            *RuntimeRequest              `json:"runtime,omitempty"`
	Retry              *pipelinev1.Retry            `json:"retry,omitempty"`
	PipelineID         string                       `json:"pipelineId"`
	PipelineRunID      string                       `json:"pipelineRunId"`
	JobRunID           string                       `json:"jobRunId"`
	JobName            string                       `json:"jobName"`
	AgentID            string                       `json:"agentId,omitempty"`
	Steps              []StepPayload                `json:"steps"`
	Env                map[string]string            `json:"env,omitempty"`
	Variables          map[string]string            `json:"variables,omitempty"`
	Workspace          string                       `json:"workspace,omitempty"`
	Timeout            string                       `json:"timeout,omitempty"`
	ArtifactURIs       map[string]string            `json:"artifactUris,omitempty"`
	ArtifactReferences map[string]ArtifactReference `json:"artifactReferences,omitempty"`
	Source             *SourcePayload               `json:"source,omitempty"`
}

// StepPayload describes a single step inside a JobRunTaskPayload.
type StepPayload struct {
	AttemptRunIDs   []string          `json:"attemptRunIds,omitempty"`
	Name            string            `json:"name"`
	StepIndex       int32             `json:"stepIndex"`
	StepRunID       string            `json:"stepRunId"`
	Uses            string            `json:"uses"`
	Action          string            `json:"action,omitempty"`
	Args            map[string]any    `json:"args,omitempty"`
	Env             map[string]string `json:"env,omitempty"`
	ContinueOnError bool              `json:"continueOnError,omitempty"`
	Timeout         string            `json:"timeout,omitempty"`
	When            string            `json:"when,omitempty"`
}

// SourcePayload describes source code configuration for a job.
type SourcePayload struct {
	// BindingDigest 绑定不可变的作业源请求，不包含认证信息
	BindingDigest string            `json:"bindingDigest,omitempty"`
	Type          string            `json:"type"`
	Repo          string            `json:"repo,omitempty"`
	Branch        string            `json:"branch,omitempty"`
	Auth          map[string]string `json:"auth,omitempty"`
	Commit        string            `json:"commit,omitempty"`
}

// JobRunKey returns a composite key for the job run task.
func JobRunKey(payload *JobRunTaskPayload) string {
	if payload == nil {
		return ""
	}
	parts := []string{
		payload.PipelineID,
		payload.PipelineRunID,
		payload.JobRunID,
	}
	return strings.Trim(strings.Join(parts, ":"), ":")
}

// ArtifactReference 绑定上游制品的存储位置、内容及有效期，不包含凭据
type ArtifactReference struct {
	StorageID   string `json:"storageId"`
	Fingerprint string `json:"fingerprint"`
	ExpiresAt   int64  `json:"expiresAt,omitempty"`
	// SHA256 保存认证登记的规范小写内容摘要
	SHA256 string `json:"sha256,omitempty"`
	// Size 使用指针区分已登记的空文件与缺少长度的历史制品
	Size *int64 `json:"size,omitempty"`
}

// ValidateIntegrity 拒绝缺失或不规范的内容元数据，历史记录不得冒充不可变输入
func (r ArtifactReference) ValidateIntegrity() error {
	if r.Size == nil || *r.Size < 0 || artifactutil.Digest(r.SHA256) != nil {
		return fmt.Errorf("artifact SHA256 and explicit non-negative size are required")
	}
	return nil
}
