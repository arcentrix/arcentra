// Copyright 2025 Arcentra Team
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

package spec

// Pipeline represents the top-level pipeline configuration
type Pipeline struct {
	// Pipeline namespace (prod, staging, org, etc.)
	Namespace string `json:"namespace"`
	// Pipeline version (semantic versioning, e.g., "1.0.0")
	Version string `json:"version,omitempty"`
	// Global environment variables
	Variables map[string]string `json:"variables,omitempty"`
	// Runtime configuration
	Runtime *Runtime `json:"runtime,omitempty"`
	// Pipeline metadata (additional metadata)
	Meta map[string]any `json:"meta,omitempty"`
	// Pipeline jobs
	Jobs []Job `json:"jobs"`
}

// Runtime represents runtime configuration for pipeline execution
type Runtime struct {
	// Runtime type (e.g., "docker", "kubernetes", "local")
	Type string `json:"type,omitempty"`
	// Runtime image (for containerized execution)
	Image string `json:"image,omitempty"`
	// Runtime environment variables
	Env map[string]string `json:"env,omitempty"`
	// Runtime resources (CPU, memory, etc.)
	Resources *Resources `json:"resources,omitempty"`
	// Runtime configuration options
	Config map[string]any `json:"config,omitempty"`
}

// Resources represents resource limits and requests
type Resources struct {
	// CPU request (e.g., "100m", "1")
	CPURequest string `json:"cpuRequest,omitempty"`
	// CPU limit (e.g., "200m", "2")
	CPULimit string `json:"cpuLimit,omitempty"`
	// Memory request (e.g., "128Mi", "1Gi")
	MemoryRequest string `json:"memoryRequest,omitempty"`
	// Memory limit (e.g., "256Mi", "2Gi")
	MemoryLimit string `json:"memoryLimit,omitempty"`
}

// Job represents a pipeline job (formerly task)
type Job struct {
	// Job name (required)
	Name string `json:"name"`
	// Job description
	Description string `json:"description,omitempty"`
	// Job environment variables
	Env map[string]string `json:"env,omitempty"`
	// Job concurrency
	Concurrency string `json:"concurrency,omitempty"` // enum: serial, parallel
	// Job timeout
	Timeout string `json:"timeout,omitempty"`
	// Job retry
	Retry *Retry `json:"retry,omitempty"`
	// Job when
	When string `json:"when,omitempty"`
	// Job source
	Source *Source `json:"source,omitempty"`
	// Job approval
	Approval *Approval `json:"approval,omitempty"`
	// Job steps
	Steps []Step `json:"steps"`
	// Job target
	Target *Target `json:"target,omitempty"`
	// Job notify
	Notify *Notify `json:"notify,omitempty"`
	// Job triggers
	Triggers []Trigger `json:"triggers,omitempty"`
	// Job dependencies (names of jobs that must complete before this job runs)
	DependsOn []string `json:"dependsOn,omitempty"`
}

// Retry configuration for job retry logic
type Retry struct {
	// Retry max attempts
	MaxAttempts int `json:"maxAttempts"`
	// Retry delay
	Delay string `json:"delay,omitempty"`
}

// Step represents a pipeline step (formerly stage)
type Step struct {
	// Step name (required)
	Name string `json:"name"`
	// Step uses (required)
	Uses string `json:"uses"`
	// Step action
	Action string `json:"action,omitempty"`
	// Step args
	Args map[string]any `json:"args,omitempty"`
	// Step environment variables
	Env map[string]string `json:"env,omitempty"`
	// Step continue on error
	ContinueOnError bool `json:"continueOnError,omitempty"`
	// Step timeout
	Timeout string `json:"timeout,omitempty"`
	// Step when
	When string `json:"when,omitempty"`
	// Agent selector for step execution
	AgentSelector *AgentSelector `json:"agentSelector,omitempty"`
	// Run on agent (if true, step runs on agent; if false, runs locally)
	RunOnAgent bool `json:"runOnAgent,omitempty"`
}

// Source represents the source configuration (Git / Artifact / S3 / Custom)
type Source struct {
	// Source type (required)
	Type string `json:"type"` // enum: git, artifact, s3, custom
	// Source repository
	Repo string `json:"repo,omitempty"`
	// Source branch
	Branch string `json:"branch,omitempty"`
	// Source authentication
	Auth *SourceAuth `json:"auth,omitempty"`
}

// SourceAuth represents authentication for source
type SourceAuth struct {
	// Source username
	Username string `json:"username,omitempty"`
	// Source password
	Password string `json:"password,omitempty"`
	// Source token
	Token string `json:"token,omitempty"`
}

// Approval represents approval configuration
type Approval struct {
	// Approval required
	Required bool `json:"required"`
	// Approval type
	Type string `json:"type,omitempty"` // enum: manual, auto
	// Approval plugin
	Plugin string `json:"plugin,omitempty"`
	// Approval params
	Params map[string]any `json:"params,omitempty"`
}

// Target represents deployment target configuration
type Target struct {
	// Target type
	Type string `json:"type"` // enum: k8s, vm, docker, s3, custom
	// Target config
	Config map[string]any `json:"config,omitempty"`
}

// Notify represents notification configuration
type Notify struct {
	// Notify on success
	OnSuccess *NotifyItem `json:"onSuccess,omitempty"`
	// Notify on failure
	OnFailure *NotifyItem `json:"onFailure,omitempty"`
}

// NotifyItem represents a notification item
type NotifyItem struct {
	// Notify plugin
	Plugin string `json:"plugin"`
	// Notify action
	Action string         `json:"action"` // e.g., "Send" or "SendTemplate"
	Params map[string]any `json:"params,omitempty"`
}

// Trigger represents a pipeline trigger
type Trigger struct {
	// Trigger type
	Type string `json:"type"` // enum: manual, cron, event
	// Trigger options
	Options map[string]any `json:"options,omitempty"`
}

// AgentSelector defines criteria for selecting an agent
type AgentSelector struct {
	// MatchLabels requires all specified labels to match (AND logic)
	MatchLabels map[string]string `json:"matchLabels,omitempty"`
	// MatchExpressions provides more complex matching rules
	MatchExpressions []LabelExpression `json:"matchExpressions,omitempty"`
}

// LabelExpression defines a label matching expression
type LabelExpression struct {
	// Label key
	Key string `json:"key"`
	// Operator: In, NotIn, Exists, NotExists, Gt, Lt
	Operator string `json:"operator"`
	// Values for operator comparison
	Values []string `json:"values,omitempty"`
}
