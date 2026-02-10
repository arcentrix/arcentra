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

package inject

import (
	tracecontext "github.com/arcentrix/arcentra/pkg/trace/context"
	"google.golang.org/grpc"
)

// GrpcServerInterceptors returns gRPC server interceptors for OpenTelemetry tracing
// Returns unary and stream server interceptors that should be added to the gRPC server chain
// These interceptors should be added at the beginning of the interceptor chain to ensure
// trace context is properly propagated
func GrpcServerInterceptors() (grpc.UnaryServerInterceptor, grpc.StreamServerInterceptor) {
	return tracecontext.UnaryServerInterceptor(), tracecontext.StreamServerInterceptor()
}

// GrpcClientInterceptors returns gRPC client interceptors for OpenTelemetry tracing
// Returns unary and stream client interceptors that should be added to the gRPC client chain
func GrpcClientInterceptors() (grpc.UnaryClientInterceptor, grpc.StreamClientInterceptor) {
	return tracecontext.UnaryClientInterceptor(), tracecontext.StreamClientInterceptor()
}

// UnaryServerInterceptor returns unary server interceptor for OpenTelemetry tracing
// This interceptor extracts trace context from incoming gRPC metadata, creates spans,
// and propagates trace context to the handler
func UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return tracecontext.UnaryServerInterceptor()
}

// StreamServerInterceptor returns stream server interceptor for OpenTelemetry tracing
// This interceptor extracts trace context from incoming gRPC metadata, creates spans,
// and propagates trace context to the stream handler
func StreamServerInterceptor() grpc.StreamServerInterceptor {
	return tracecontext.StreamServerInterceptor()
}

// UnaryClientInterceptor returns unary client interceptor for OpenTelemetry tracing
// This interceptor injects trace context into outgoing gRPC metadata and creates spans
// for client-side RPC calls
func UnaryClientInterceptor() grpc.UnaryClientInterceptor {
	return tracecontext.UnaryClientInterceptor()
}

// StreamClientInterceptor returns stream client interceptor for OpenTelemetry tracing
// This interceptor injects trace context into outgoing gRPC metadata and creates spans
// for client-side streaming RPC calls
func StreamClientInterceptor() grpc.StreamClientInterceptor {
	return tracecontext.StreamClientInterceptor()
}
