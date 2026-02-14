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
	"time"

	"github.com/arcentrix/arcentra/pkg/log"
	"google.golang.org/grpc"
)

// excluded methods that don't need logging
var excludedMethods = map[string]bool{
	// "/api.agent.v1.Agent/Heartbeat":  true,
	// "/api.job.v1.Job/Ping":           true,
	// "/api.stream.v1.Stream/Ping":     true,
	// "/api.pipeline.v1.Pipeline/Ping": true,
	"/agent.v1.AgentService/Heartbeat": true,
}

// logCall logs a gRPC server call with common format
func logCall(callType string, method string, duration time.Duration, err error) {
	if err != nil {
		log.Errorw("gRPC call failed", "type", callType, "error", err)
	} else {
		log.Debugw("gRPC call", "type", callType, "method", method, "duration", duration.String())
	}
}

// logClientCall logs a gRPC client call with common format
func logClientCall(callType string, method string, duration time.Duration, err error) {
	if err != nil {
		log.Errorw("gRPC client call failed", "type", callType, "error", err)
	} else {
		log.Debugw("gRPC client call", "type", callType, "method", method, "duration", duration.String())
	}
}

// LoggingUnaryInterceptor unary server interceptor (can filter heartbeat interface)
func LoggingUnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		if excludedMethods[info.FullMethod] {
			return handler(ctx, req)
		}

		start := time.Now()
		resp, err := handler(ctx, req)
		duration := time.Since(start)

		logCall("unary", info.FullMethod, duration, err)

		return resp, err
	}
}

// LoggingStreamInterceptor stream server interceptor (can filter heartbeat interface)
func LoggingStreamInterceptor() grpc.StreamServerInterceptor {
	return func(
		srv any,
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		if excludedMethods[info.FullMethod] {
			return handler(srv, ss)
		}

		start := time.Now()
		err := handler(srv, ss)
		duration := time.Since(start)

		logCall("stream", info.FullMethod, duration, err)

		return err
	}
}

// LoggingUnaryClientInterceptor unary client interceptor
func LoggingUnaryClientInterceptor() grpc.UnaryClientInterceptor {
	return func(
		ctx context.Context,
		method string,
		req, reply any,
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		start := time.Now()
		err := invoker(ctx, method, req, reply, cc, opts...)
		duration := time.Since(start)

		logClientCall("unary", method, duration, err)

		return err
	}
}

// LoggingStreamClientInterceptor stream client interceptor
func LoggingStreamClientInterceptor() grpc.StreamClientInterceptor {
	return func(
		ctx context.Context,
		desc *grpc.StreamDesc,
		cc *grpc.ClientConn,
		method string,
		streamer grpc.Streamer,
		opts ...grpc.CallOption,
	) (grpc.ClientStream, error) {
		start := time.Now()
		stream, err := streamer(ctx, desc, cc, method, opts...)
		duration := time.Since(start)

		logClientCall("stream", method, duration, err)

		return stream, err
	}
}
