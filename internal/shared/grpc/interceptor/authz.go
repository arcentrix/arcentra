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

package interceptor

import (
	"context"
	"strings"

	"github.com/arcentrix/arcentra/internal/control/authz"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var methodActionMap = map[string]string{
	"/api.agent.v1.Agent/Approve": "agent:approve",
	"/api.agent.v1.Agent/List":    "agent:read",
	"/api.agent.v1.Agent/Get":     "agent:read",
	"/api.agent.v1.Agent/Delete":  "agent:delete",
}

var grpcAuthorizer authz.IAuthorizer

// SetAuthorizer 设置 gRPC 授权器
func SetAuthorizer(authorizer authz.IAuthorizer) {
	grpcAuthorizer = authorizer
}

// AuthzUnaryInterceptor gRPC 一元授权拦截器
func AuthzUnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		if excludedAuthMethods[info.FullMethod] {
			return handler(ctx, req)
		}
		if grpcAuthorizer == nil {
			return handler(ctx, req)
		}
		action := methodActionMap[info.FullMethod]
		if action == "" {
			return handler(ctx, req)
		}
		tokenInfo, ok := GetTokenInfofromContext(ctx)
		if !ok || tokenInfo == nil {
			return nil, status.Error(codes.Unauthenticated, "token info missing")
		}

		subject := authz.Subject{
			UserID: tokenInfo.ID,
		}
		for _, role := range tokenInfo.Roles {
			if strings.EqualFold(role, "agent") {
				subject.IsAgent = true
				break
			}
		}
		allowed, err := grpcAuthorizer.Check(ctx, subject, action, authz.ResourceRef{})
		if err != nil {
			return nil, status.Errorf(codes.Internal, "authz check failed: %v", err)
		}
		if !allowed {
			return nil, status.Error(codes.PermissionDenied, "permission denied")
		}
		return handler(ctx, req)
	}
}

// AuthzStreamInterceptor gRPC 流式授权拦截器
func AuthzStreamInterceptor() grpc.StreamServerInterceptor {
	return func(
		srv any,
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		if excludedAuthMethods[info.FullMethod] {
			return handler(srv, ss)
		}
		if grpcAuthorizer == nil {
			return handler(srv, ss)
		}
		action := methodActionMap[info.FullMethod]
		if action == "" {
			return handler(srv, ss)
		}
		tokenInfo, ok := GetTokenInfofromContext(ss.Context())
		if !ok || tokenInfo == nil {
			return status.Error(codes.Unauthenticated, "token info missing")
		}
		subject := authz.Subject{
			UserID: tokenInfo.ID,
		}
		for _, role := range tokenInfo.Roles {
			if strings.EqualFold(role, "agent") {
				subject.IsAgent = true
				break
			}
		}
		allowed, err := grpcAuthorizer.Check(ss.Context(), subject, action, authz.ResourceRef{})
		if err != nil {
			return status.Errorf(codes.Internal, "authz check failed: %v", err)
		}
		if !allowed {
			return status.Error(codes.PermissionDenied, "permission denied")
		}
		return handler(srv, ss)
	}
}
