// Copyright 2025 Arcade Team
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

package service

import (
	"context"
	"errors"
	"strings"
	"time"

	agentv1 "github.com/arcentrix/arcade/api/agent/v1"
	agentmodel "github.com/arcentrix/arcade/internal/engine/model"
	"github.com/arcentrix/arcade/pkg/log"
	"github.com/bytedance/sonic"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

type AgentServiceImpl struct {
	agentv1.UnimplementedAgentServiceServer
	agentService *AgentService
}

func NewAgentServiceImpl(agentService *AgentService) *AgentServiceImpl {
	return &AgentServiceImpl{
		agentService: agentService,
	}
}

func (a *AgentServiceImpl) Heartbeat(ctx context.Context, req *agentv1.HeartbeatRequest) (*agentv1.HeartbeatResponse, error) {

	resp := &agentv1.HeartbeatResponse{
		Success:   true,
		Message:   "pong",
		Timestamp: time.Now().Unix(),
	}

	return resp, nil
}

func (a *AgentServiceImpl) Register(ctx context.Context, req *agentv1.RegisterRequest) (*agentv1.RegisterResponse, error) {
	// Validate token
	if req.Token == "" {
		return nil, status.Errorf(codes.InvalidArgument, "token is required")
	}

	agentRepo := a.agentService.agentRepo
	var agentId string
	var existingAgent *agentmodel.Agent
	var err error

	// Extract agentId from token (token format: agentId:signature)
	// If request provides agentId, use it for validation; otherwise extract from token
	tokenParts := strings.SplitN(req.Token, ":", 2)
	if len(tokenParts) != 2 {
		return nil, status.Errorf(codes.InvalidArgument, "invalid token format: expected agentId:signature")
	}
	tokenAgentId := tokenParts[0]

	// Use agentId from request if provided, otherwise use agentId from token
	if req.AgentId != "" {
		agentId = req.AgentId
		// Validate that request agentId matches token agentId
		if agentId != tokenAgentId {
			log.Warnw("agentId mismatch", "requestAgentId", agentId, "tokenAgentId", tokenAgentId)
			return nil, status.Errorf(codes.InvalidArgument, "agentId mismatch: request agentId does not match token")
		}
	} else {
		agentId = tokenAgentId
	}

	// Verify token by regenerating it and comparing
	expectedToken, err := a.agentService.GenerateAgentToken(agentId)
	if err != nil {
		log.Errorw("failed to generate token for verification", "agentId", agentId, "error", err)
		return nil, status.Errorf(codes.Internal, "failed to verify token")
	}

	if req.Token != expectedToken {
		log.Warnw("token verification failed", "agentId", agentId)
		return nil, status.Errorf(codes.Unauthenticated, "invalid token")
	}

	// Check if agent exists
	existingAgent, err = agentRepo.GetAgentByAgentId(agentId)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, status.Errorf(codes.NotFound, "agent not found: %s", agentId)
		}
		log.Errorw("failed to get agent", "agentId", agentId, "error", err)
		return nil, status.Errorf(codes.Internal, "failed to get agent")
	}

	// Update agent information from registration request
	updates := make(map[string]any)

	if req.Ip != "" {
		updates["address"] = req.Ip
	}
	if req.Os != "" {
		updates["os"] = req.Os
	}
	if req.Arch != "" {
		updates["arch"] = req.Arch
	}
	if req.Version != "" {
		updates["version"] = req.Version
	}
	if len(req.Labels) > 0 {
		updates["labels"] = req.Labels
	}
	updates["status"] = 1 // Set status to online
	updates["last_heartbeat"] = time.Now()

	if len(updates) > 0 {
		if err := agentRepo.UpdateAgentById(existingAgent.ID, updates); err != nil {
			log.Errorw("failed to update agent during registration", "agentId", agentId, "error", err)
			return nil, status.Errorf(codes.Internal, "failed to update agent")
		}
	}

	// Get agent detail to return heartbeat interval
	detail, err := agentRepo.GetAgentDetailById(existingAgent.ID)
	if err != nil {
		log.Errorw("failed to get agent detail", "agentId", agentId, "error", err)
		return nil, status.Errorf(codes.Internal, "failed to get agent detail")
	}

	heartbeatInterval := int64(60) // default

	// Parse labels from JSON
	labels := make(map[string]string)
	if len(detail.Labels) > 0 {
		if err := sonic.Unmarshal(detail.Labels, &labels); err != nil {
			log.Warnw("failed to parse labels", "agentId", agentId, "error", err)
			// Continue with empty labels if parsing fails
		}
	}

	return &agentv1.RegisterResponse{
		Success:           true,
		Message:           "registration successful",
		AgentId:           agentId,
		HeartbeatInterval: heartbeatInterval,
		Labels:            labels,
	}, nil
}
