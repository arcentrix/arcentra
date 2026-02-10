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

package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	executor "github.com/arcentrix/arcentra/internal/pkg/executor"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/plugin"
	"github.com/bytedance/sonic"
)

// ApprovalStatus represents the status of an approval
type ApprovalStatus string

const (
	ApprovalStatusPending  ApprovalStatus = "pending"
	ApprovalStatusApproved ApprovalStatus = "approved"
	ApprovalStatusRejected ApprovalStatus = "rejected"
	ApprovalStatusExpired  ApprovalStatus = "expired"
)

// ApprovalRequest represents an approval request
type ApprovalRequest struct {
	ID          string         `json:"id"`
	JobName     string         `json:"job_name"`
	StepName    string         `json:"step_name,omitempty"`
	Plugin      string         `json:"plugin"`
	Params      map[string]any `json:"params"`
	Status      ApprovalStatus `json:"status"`
	RequestedAt time.Time      `json:"requested_at"`
	ExpiresAt   *time.Time     `json:"expires_at,omitempty"`
	ApprovedBy  string         `json:"approved_by,omitempty"`
	RejectedBy  string         `json:"rejected_by,omitempty"`
	Reason      string         `json:"reason,omitempty"`
}

// ApprovalManager manages approval workflow
type ApprovalManager struct {
	mu           sync.RWMutex
	requests     map[string]*ApprovalRequest
	pluginMgr    *plugin.Manager
	logger       log.Logger
	timeout      time.Duration
	pollInterval time.Duration
	emitter      *executor.EventEmitter
}

// NewApprovalManager creates a new approval manager
func NewApprovalManager(pluginMgr *plugin.Manager, logger log.Logger) *ApprovalManager {
	return &ApprovalManager{
		requests:     make(map[string]*ApprovalRequest),
		pluginMgr:    pluginMgr,
		logger:       logger,
		timeout:      24 * time.Hour,  // Default timeout: 24 hours
		pollInterval: 5 * time.Second, // Default poll interval: 5 seconds
	}
}

// SetEventEmitter sets the event emitter for approval events.
func (am *ApprovalManager) SetEventEmitter(emitter *executor.EventEmitter) {
	am.mu.Lock()
	defer am.mu.Unlock()
	am.emitter = emitter
}

// SetTimeout sets the default timeout for approval requests
func (am *ApprovalManager) SetTimeout(timeout time.Duration) {
	am.mu.Lock()
	defer am.mu.Unlock()
	am.timeout = timeout
}

// SetPollInterval sets the polling interval for checking approval status
func (am *ApprovalManager) SetPollInterval(interval time.Duration) {
	am.mu.Lock()
	defer am.mu.Unlock()
	am.pollInterval = interval
}

// CreateApproval creates a new approval request
func (am *ApprovalManager) CreateApproval(ctx context.Context, jobName, stepName, pluginName string, params map[string]any) (*ApprovalRequest, error) {
	requestID := fmt.Sprintf("%s-%s-%d", jobName, stepName, time.Now().UnixNano())

	expiresAt := time.Now().Add(am.timeout)
	request := &ApprovalRequest{
		ID:          requestID,
		JobName:     jobName,
		StepName:    stepName,
		Plugin:      pluginName,
		Params:      params,
		Status:      ApprovalStatusPending,
		RequestedAt: time.Now(),
		ExpiresAt:   &expiresAt,
	}

	am.mu.Lock()
	am.requests[requestID] = request
	am.mu.Unlock()

	// Call approval plugin to create approval
	if err := am.createApprovalInPlugin(request); err != nil {
		am.mu.Lock()
		delete(am.requests, requestID)
		am.mu.Unlock()
		return nil, fmt.Errorf("create approval in plugin: %w", err)
	}

	if am.logger.Log != nil {
		am.logger.Log.Infow("created approval request", "request", requestID, "job", jobName, "step", stepName)
	}
	am.emitApprovalEvent(plugin.EventTypePipelineApprovalRequested, request, "")

	return request, nil
}

// WaitForApproval waits for approval to be approved or rejected
func (am *ApprovalManager) WaitForApproval(ctx context.Context, requestID string) (bool, error) {
	ticker := time.NewTicker(am.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-ticker.C:
			request, err := am.GetApproval(requestID)
			if err != nil {
				return false, err
			}

			// Check expiration
			if request.ExpiresAt != nil && time.Now().After(*request.ExpiresAt) {
				am.mu.Lock()
				request.Status = ApprovalStatusExpired
				am.mu.Unlock()
				return false, fmt.Errorf("approval request expired")
			}

			switch request.Status {
			case ApprovalStatusApproved:
				return true, nil
			case ApprovalStatusRejected:
				return false, fmt.Errorf("approval rejected: %s", request.Reason)
			case ApprovalStatusExpired:
				return false, fmt.Errorf("approval expired")
			case ApprovalStatusPending:
				// Continue polling
				continue
			}
		}
	}
}

// GetApproval gets an approval request by ID
func (am *ApprovalManager) GetApproval(requestID string) (*ApprovalRequest, error) {
	am.mu.RLock()
	defer am.mu.RUnlock()

	request, ok := am.requests[requestID]
	if !ok {
		return nil, fmt.Errorf("approval request not found: %s", requestID)
	}

	return request, nil
}

// Approve approves an approval request
func (am *ApprovalManager) Approve(requestID, approvedBy, reason string) error {
	am.mu.Lock()
	defer am.mu.Unlock()

	request, ok := am.requests[requestID]
	if !ok {
		return fmt.Errorf("approval request not found: %s", requestID)
	}

	if request.Status != ApprovalStatusPending {
		return fmt.Errorf("approval request is not pending: %s", request.Status)
	}

	request.Status = ApprovalStatusApproved
	request.ApprovedBy = approvedBy
	request.Reason = reason

	if am.logger.Log != nil {
		am.logger.Log.Infow("approval request approved", "request", requestID, "approved_by", approvedBy)
	}
	am.emitApprovalEvent(plugin.EventTypePipelineApprovalApproved, request, reason)

	return nil
}

// Reject rejects an approval request
func (am *ApprovalManager) Reject(requestID, rejectedBy, reason string) error {
	am.mu.Lock()
	defer am.mu.Unlock()

	request, ok := am.requests[requestID]
	if !ok {
		return fmt.Errorf("approval request not found: %s", requestID)
	}

	if request.Status != ApprovalStatusPending {
		return fmt.Errorf("approval request is not pending: %s", request.Status)
	}

	request.Status = ApprovalStatusRejected
	request.RejectedBy = rejectedBy
	request.Reason = reason

	if am.logger.Log != nil {
		am.logger.Log.Infow("approval request rejected", "request", requestID, "rejected_by", rejectedBy, "reason", reason)
	}
	am.emitApprovalEvent(plugin.EventTypePipelineApprovalRejected, request, reason)

	return nil
}

func (am *ApprovalManager) emitApprovalEvent(eventType string, request *ApprovalRequest, reason string) {
	am.mu.RLock()
	emitter := am.emitter
	am.mu.RUnlock()
	if emitter == nil || request == nil {
		return
	}
	data := map[string]any{
		"requestId": request.ID,
		"jobName":   request.JobName,
		"stepName":  request.StepName,
		"status":    string(request.Status),
	}
	if reason != "" {
		data["reason"] = reason
	}
	emitter.Emit(context.Background(), eventType, emitter.BuildSource("pipeline"), fmt.Sprintf("approval:%s", request.ID), data, nil)
}

// ListApprovals lists all approval requests
func (am *ApprovalManager) ListApprovals() []*ApprovalRequest {
	am.mu.RLock()
	defer am.mu.RUnlock()

	requests := make([]*ApprovalRequest, 0, len(am.requests))
	for _, request := range am.requests {
		requests = append(requests, request)
	}

	return requests
}

// CleanupExpiredApprovals removes expired approval requests
func (am *ApprovalManager) CleanupExpiredApprovals() int {
	am.mu.Lock()
	defer am.mu.Unlock()

	now := time.Now()
	cleaned := 0

	for _, request := range am.requests {
		if request.ExpiresAt != nil && now.After(*request.ExpiresAt) {
			if request.Status == ApprovalStatusPending {
				request.Status = ApprovalStatusExpired
			}
			cleaned++
		}
	}

	return cleaned
}

// createApprovalInPlugin creates approval request in the plugin
func (am *ApprovalManager) createApprovalInPlugin(request *ApprovalRequest) error {
	pluginClient, err := am.pluginMgr.GetPlugin(request.Plugin)
	if err != nil {
		return fmt.Errorf("approval plugin not found: %s: %w", request.Plugin, err)
	}

	paramsJSON, err := json.Marshal(request.Params)
	if err != nil {
		return fmt.Errorf("marshal approval params: %w", err)
	}

	// Add request metadata to params
	metadata := map[string]any{
		"id":           request.ID,
		"job_name":     request.JobName,
		"step_name":    request.StepName,
		"requested_at": request.RequestedAt,
		"expires_at":   request.ExpiresAt,
	}
	metadataJSON, err := sonic.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("marshal approval metadata: %w", err)
	}

	_, err = pluginClient.Execute("approval.create", paramsJSON, metadataJSON)
	return err
}
