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

package interceptor

import (
	"context"
	"fmt"
	"github.com/arcentrix/arcentra/internal/shared/agentidentity"
	"reflect"

	grpcauth "github.com/grpc-ecosystem/go-grpc-middleware/auth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type TokenInfo = agentidentity.Identity

// excluded methods that need to skip authentication
var excludedAuthMethods = map[string]bool{
	// 注册请求在服务层验证一次性注册令牌或已有 Agent 凭证
	"/agent.v1.AgentService/Register": true,
}

// Agent 凭证仅允许执行工作协议；管理 API 通过 HTTP 用户权限校验
var agentUnaryMethods = map[string]bool{
	"/agent.v1.AgentService/ClaimAndFetchCopilotRerun": true,
	"/agent.v1.AgentService/ReconcileStepAttempt":      true,
	"/agent.v1.AgentService/ReconcileJobRetryApproval": true,
	"/agent.v1.AgentService/ReconcileJobGeneration":    true,
	"/agent.v1.AgentService/Heartbeat":                 true,
	"/agent.v1.AgentService/Unregister":                true,
	"/agent.v1.AgentService/FetchStepRun":              true,
	"/agent.v1.AgentService/ReportStepRunStatus":       true,
	"/agent.v1.AgentService/ReportJobRunStatus":        true,
	"/agent.v1.AgentService/InspectJobExecution":       true,
	"/agent.v1.AgentService/GetOrRecordJobInputs":      true,
}
var agentStreamMethods = map[string]bool{
	"/gateway.v1.GatewayService/PushLogs":   true,
	"/gateway.v1.GatewayService/PushEvents": true,
}

// AuthUnaryInterceptor unary server interceptor (can skip heartbeat interface)
func AuthUnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		// check if need to skip authentication
		if excludedAuthMethods[info.FullMethod] {
			return handler(ctx, req)
		}

		// execute authentication with request context
		token, err := grpcauth.AuthFromMD(ctx, "bearer")
		if err != nil {
			return nil, err
		}
		tokenInfo, err := parseToken(ctx, token, req)
		if err != nil {
			return nil, status.Errorf(codes.Unauthenticated, " %v", err)
		}
		if !agentUnaryMethods[info.FullMethod] {
			return nil, status.Error(codes.PermissionDenied, "agent credentials cannot access this RPC")
		}
		// use custom type as key, avoid conflict with other package's context key
		newCtx := agentidentity.WithIdentity(ctx, tokenInfo)
		return handler(newCtx, req)
	}
}

// AuthStreamInterceptor stream server interceptor (can skip heartbeat interface)
func AuthStreamInterceptor() grpc.StreamServerInterceptor {
	return func(
		srv any,
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		// check if need to skip authentication
		if excludedAuthMethods[info.FullMethod] {
			return handler(srv, ss)
		}

		// execute authentication with stream context
		token, err := grpcauth.AuthFromMD(ss.Context(), "bearer")
		if err != nil {
			return status.Errorf(codes.Unauthenticated, "failed to get token: %v", err)
		}
		tokenInfo, err := parseToken(ss.Context(), token, nil)
		if err != nil {
			return status.Errorf(codes.Unauthenticated, " %v", err)
		}
		if !agentStreamMethods[info.FullMethod] {
			return status.Error(codes.PermissionDenied, "agent credentials cannot access this stream")
		}
		newCtx := agentidentity.WithIdentity(ss.Context(), tokenInfo)

		// create new ServerStream wrapper
		wrapped := &wrappedServerStream{
			ServerStream: ss,
			ctx:          newCtx,
		}

		return handler(srv, wrapped)
	}
}

// wrappedServerStream wrap ServerStream to replace context
type wrappedServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrappedServerStream) Context() context.Context {
	return w.ctx
}

func AuthInterceptor(ctx context.Context) (context.Context, error) {
	token, err := grpcauth.AuthFromMD(ctx, "bearer")
	if err != nil {
		return nil, err
	}
	tokenInfo, err := parseToken(ctx, token, nil)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, " %v", err)
	}
	// use custom type as key, avoid conflict with other package's context key
	newCtx := agentidentity.WithIdentity(ctx, tokenInfo)
	return newCtx, nil
}

// GetTokenInfofromContext get TokenInfo from context
func GetTokenInfofromContext(ctx context.Context) (*TokenInfo, bool) {
	return agentidentity.FromContext(ctx)
}

// TokenVerifier interface for token verification
// This allows different implementations (config-based, database-based, etc.)
type TokenVerifier interface {
	// VerifyAgentToken verifies a token generated by generateAgentToken and returns agent ID if valid
	// agentIDHint: optional agent ID from request, if provided, verify this agent first (optimization)
	// Returns empty string if token is invalid or agent not found
	VerifyAgentToken(ctx context.Context, token string, agentIDHint string) (string, error)
}

// defaultTokenVerifier default token verifier using hardcoded token
type defaultTokenVerifier struct{}

func (v *defaultTokenVerifier) VerifyAgentToken(_ context.Context, _ string, _ string) (string, error) {
	// Default implementation: return empty to indicate token verification not supported
	return "", fmt.Errorf("agent token verification not implemented")
}

var tokenVerifier TokenVerifier = &defaultTokenVerifier{}

// SetTokenVerifier sets the token verifier (can be called during initialization)
func SetTokenVerifier(verifier TokenVerifier) {
	tokenVerifier = verifier
}

// parse token and validate
// req can be nil, but if provided and contains agent_id, it will be used for optimization
func parseToken(ctx context.Context, token string, req any) (TokenInfo, error) {
	var tokenInfo TokenInfo

	// Try to extract agent_id from request if available (optimization)
	var agentIDFromReq string
	if req != nil {
		agentIDFromReq = extractAgentIDFromRequest(req)
	}

	// Verify as agent token (generated by generateAgentToken using HMAC-SHA256)
	agentID, err := verifyAgentToken(ctx, token, agentIDFromReq)
	if err == nil && agentID != "" {
		if agentIDFromReq != "" && agentIDFromReq != agentID {
			return tokenInfo, fmt.Errorf("agent identity mismatch")
		}
		tokenInfo.ID = agentID
		tokenInfo.Roles = []string{"agent"} // Default role for agents
		return tokenInfo, nil
	}

	return tokenInfo, fmt.Errorf("invalid token: %v", err)
}

// extractAgentIDFromRequest extracts agent_id from request using reflection
func extractAgentIDFromRequest(req any) string {
	if req == nil {
		return ""
	}

	// Use reflection to extract agent_id field
	rv := reflect.ValueOf(req)
	if rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}

	if rv.Kind() != reflect.Struct {
		return ""
	}

	// Try common field names: AgentId, AgentID, agent_id
	fieldNames := []string{"AgentId", "AgentID", "agent_id"}
	for _, fieldName := range fieldNames {
		field := rv.FieldByName(fieldName)
		if field.IsValid() && field.Kind() == reflect.String {
			agentID := field.String()
			if agentID != "" {
				return agentID
			}
		}
	}

	// Try GetAgentId() method if exists
	method := rv.MethodByName("GetAgentId")
	if !method.IsValid() {
		method = rv.MethodByName("GetAgentID")
	}
	if method.IsValid() && method.Type().NumIn() == 0 && method.Type().NumOut() == 1 {
		results := method.Call(nil)
		if len(results) > 0 {
			if agentID, ok := results[0].Interface().(string); ok && agentID != "" {
				return agentID
			}
		}
	}

	return ""
}

// verifyAgentToken verifies a token generated by generateAgentToken
// The token is generated using HMAC-SHA256 with secret key and salt from database
// Reference: internal/process/service/service_agent.go generateAgentToken
// agentIDHint: optional agent ID from request, if provided, verify this agent first
func verifyAgentToken(ctx context.Context, token string, agentIDHint string) (string, error) {
	// Use TokenVerifier to verify the token
	agentID, err := tokenVerifier.VerifyAgentToken(ctx, token, agentIDHint)
	if err != nil {
		return "", fmt.Errorf("failed to verify agent token: %w", err)
	}
	if agentID == "" {
		return "", fmt.Errorf("agent token verification failed: agent not found")
	}
	return agentID, nil
}
