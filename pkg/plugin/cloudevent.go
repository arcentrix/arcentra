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

package plugin

import (
	"time"

	"github.com/google/uuid"
)

const (
	// CloudEventSpecVersion is the CloudEvents spec version.
	CloudEventSpecVersion = "1.0"
	// CloudEventContentTypeJSON is the default data content type.
	CloudEventContentTypeJSON = "application/json"
)

const (
	// EventTypeTaskSubmitted indicates a task was submitted.
	EventTypeTaskSubmitted = "arcentra.task.submitted"
	// EventTypeTaskScheduled indicates a task was scheduled.
	EventTypeTaskScheduled = "arcentra.task.scheduled"
	// EventTypeTaskStarted indicates a task started.
	EventTypeTaskStarted = "arcentra.task.started"
	// EventTypeTaskProgress indicates a task progress update.
	EventTypeTaskProgress = "arcentra.task.progress"
	// EventTypeTaskLog indicates a task log update.
	EventTypeTaskLog = "arcentra.task.log"
	// EventTypeTaskArtifact indicates a task artifact update.
	EventTypeTaskArtifact = "arcentra.task.artifact"
	// EventTypeTaskSucceeded indicates a task succeeded.
	EventTypeTaskSucceeded = "arcentra.task.succeeded"
	// EventTypeTaskFailed indicates a task failed.
	EventTypeTaskFailed = "arcentra.task.failed"
	// EventTypeTaskFinished indicates a task finished.
	EventTypeTaskFinished = "arcentra.task.finished"
	// EventTypeTaskApprovalRequested indicates an approval request.
	EventTypeTaskApprovalRequested = "arcentra.task.approval.requested"
	// EventTypeTaskApprovalApproved indicates an approval was granted.
	EventTypeTaskApprovalApproved = "arcentra.task.approval.approved"
	// EventTypeTaskApprovalRejected indicates an approval was rejected.
	EventTypeTaskApprovalRejected = "arcentra.task.approval.rejected"
	// EventTypeTaskRollbackRequested indicates a rollback request.
	EventTypeTaskRollbackRequested = "arcentra.task.rollback.requested"
	// EventTypeTaskRollbackStarted indicates a rollback start.
	EventTypeTaskRollbackStarted = "arcentra.task.rollback.started"
	// EventTypeTaskRollbackFinished indicates a rollback finish.
	EventTypeTaskRollbackFinished = "arcentra.task.rollback.finished"
	// EventTypeTaskRetryRequested indicates a retry request.
	EventTypeTaskRetryRequested = "arcentra.task.retry.requested"
	// EventTypeTaskRetryStarted indicates a retry start.
	EventTypeTaskRetryStarted = "arcentra.task.retry.started"
	// EventTypePipelineStarted indicates a pipeline started.
	EventTypePipelineStarted = "arcentra.pipeline.started"
	// EventTypePipelineCompleted indicates a pipeline completed.
	EventTypePipelineCompleted = "arcentra.pipeline.completed"
	// EventTypePipelineFailed indicates a pipeline failed.
	EventTypePipelineFailed = "arcentra.pipeline.failed"
	// EventTypePipelineCancelled indicates a pipeline cancelled.
	EventTypePipelineCancelled = "arcentra.pipeline.cancelled"
	// EventTypePipelineApprovalRequested indicates a pipeline approval requested.
	EventTypePipelineApprovalRequested = "arcentra.pipeline.approval.requested"
	// EventTypePipelineApprovalApproved indicates a pipeline approval approved.
	EventTypePipelineApprovalApproved = "arcentra.pipeline.approval.approved"
	// EventTypePipelineApprovalRejected indicates a pipeline approval rejected.
	EventTypePipelineApprovalRejected = "arcentra.pipeline.approval.rejected"
	// EventTypePipelineRollbackRequested indicates a pipeline rollback requested.
	EventTypePipelineRollbackRequested = "arcentra.pipeline.rollback.requested"
	// EventTypePipelineRollbackStarted indicates a pipeline rollback started.
	EventTypePipelineRollbackStarted = "arcentra.pipeline.rollback.started"
	// EventTypePipelineRollbackFinished indicates a pipeline rollback finished.
	EventTypePipelineRollbackFinished = "arcentra.pipeline.rollback.finished"
	// EventTypeJobStarted indicates a job started.
	EventTypeJobStarted = "arcentra.job.started"
	// EventTypeJobCompleted indicates a job completed.
	EventTypeJobCompleted = "arcentra.job.completed"
	// EventTypeJobFailed indicates a job failed.
	EventTypeJobFailed = "arcentra.job.failed"
	// EventTypeJobCancelled indicates a job cancelled.
	EventTypeJobCancelled = "arcentra.job.cancelled"
	// EventTypeStepStarted indicates a step started.
	EventTypeStepStarted = "arcentra.step.started"
	// EventTypeStepCompleted indicates a step completed.
	EventTypeStepCompleted = "arcentra.step.completed"
	// EventTypeStepFailed indicates a step failed.
	EventTypeStepFailed = "arcentra.step.failed"
	// EventTypeStepCancelled indicates a step cancelled.
	EventTypeStepCancelled = "arcentra.step.cancelled"
)

// CloudEvent represents a CloudEvents 1.0 envelope.
type CloudEvent struct {
	SpecVersion     string         `json:"specversion"`
	Id              string         `json:"id"`
	Source          string         `json:"source"`
	Type            string         `json:"type"`
	Time            time.Time      `json:"time"`
	DataContentType string         `json:"datacontenttype,omitempty"`
	Subject         string         `json:"subject,omitempty"`
	Data            any            `json:"data,omitempty"`
	Extensions      map[string]any `json:"-"`
}

// CloudEventOption defines a functional option for CloudEvent.
type CloudEventOption func(*CloudEvent)

// WithCloudEventId sets the CloudEvent id.
func WithCloudEventId(id string) CloudEventOption {
	return func(e *CloudEvent) {
		e.Id = id
	}
}

// WithCloudEventTime sets the CloudEvent time.
func WithCloudEventTime(t time.Time) CloudEventOption {
	return func(e *CloudEvent) {
		e.Time = t
	}
}

// WithCloudEventSubject sets the CloudEvent subject.
func WithCloudEventSubject(subject string) CloudEventOption {
	return func(e *CloudEvent) {
		e.Subject = subject
	}
}

// WithCloudEventContentType sets the CloudEvent data content type.
func WithCloudEventContentType(contentType string) CloudEventOption {
	return func(e *CloudEvent) {
		e.DataContentType = contentType
	}
}

// WithCloudEventExtensions sets the CloudEvent extension attributes.
func WithCloudEventExtensions(extensions map[string]any) CloudEventOption {
	return func(e *CloudEvent) {
		if len(extensions) == 0 {
			return
		}
		if e.Extensions == nil {
			e.Extensions = make(map[string]any)
		}
		for k, v := range extensions {
			if k == "" {
				continue
			}
			e.Extensions[k] = v
		}
	}
}

// NewCloudEvent creates a CloudEvent with defaults.
func NewCloudEvent(eventType, source string, data any, opts ...CloudEventOption) *CloudEvent {
	event := &CloudEvent{
		SpecVersion:     CloudEventSpecVersion,
		Id:              uuid.NewString(),
		Source:          source,
		Type:            eventType,
		Time:            time.Now(),
		DataContentType: CloudEventContentTypeJSON,
		Data:            data,
	}
	for _, opt := range opts {
		opt(event)
	}
	return event
}

// ToMap converts CloudEvent to a map for serialization with extensions.
func (e *CloudEvent) ToMap() map[string]any {
	result := map[string]any{
		"specversion":     e.SpecVersion,
		"id":              e.Id,
		"source":          e.Source,
		"type":            e.Type,
		"time":            e.Time,
		"datacontenttype": e.DataContentType,
	}
	if e.Subject != "" {
		result["subject"] = e.Subject
	}
	if e.Data != nil {
		result["data"] = e.Data
	}
	if len(e.Extensions) > 0 {
		for k, v := range e.Extensions {
			if k == "" {
				continue
			}
			result[k] = v
		}
	}
	return result
}
