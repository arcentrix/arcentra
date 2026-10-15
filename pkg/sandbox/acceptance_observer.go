//go:build sandbox_acceptance

// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package sandbox

import (
	"context"
	"fmt"
	"github.com/containerd/containerd"
	containersapi "github.com/containerd/containerd/api/services/containers/v1"
	tasksapi "github.com/containerd/containerd/api/services/tasks/v1"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/pkg/dialer"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials/insecure"
	"os"
	"strings"
	"sync"
	"time"
)

var acceptanceObservers = struct {
	sync.RWMutex
	callbacks map[string]func(string, string)
}{callbacks: make(map[string]func(string, string))}

// ObserveAcceptanceLifecycle 仅在验收构建中记录临时命名空间的实际生命周期 RPC 尝试
// 观察器不能改写请求、响应、错误或增加重试，不记录环境、命令或凭据
func ObserveAcceptanceLifecycle(namespace string, observer func(method, resourceID string)) (func(), error) {
	if os.Getenv("ARCENTRA_CONTAINERD_ACCEPTANCE") != "1" || os.Getenv("ARCENTRA_REQUIRE_CONTAINERD_ACCEPTANCE") != "1" || !strings.HasPrefix(namespace, "arcentra-containerjob-acceptance-") || observer == nil {
		return nil, fmt.Errorf("explicit dedicated acceptance observer required")
	}
	acceptanceObservers.Lock()
	defer acceptanceObservers.Unlock()
	if _, exists := acceptanceObservers.callbacks[namespace]; exists {
		return nil, fmt.Errorf("acceptance observer already installed")
	}
	acceptanceObservers.callbacks[namespace] = observer
	var once sync.Once
	return func() {
		once.Do(func() {
			acceptanceObservers.Lock()
			delete(acceptanceObservers.callbacks, namespace)
			acceptanceObservers.Unlock()
		})
	}, nil
}
func acceptanceContainerdClientOptions() []containerd.ClientOpt {
	// Pinned containerd v1.7.31 WithDialOpts REPLACES its defaults. Preserve
	// the exact official Unix-socket dial defaults; add only observation.
	policy := backoff.DefaultConfig
	policy.MaxDelay = 3 * time.Second
	return []containerd.ClientOpt{containerd.WithDialOpts([]grpc.DialOption{
		grpc.WithBlock(), grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.FailOnNonTempDialError(true), grpc.WithConnectParams(grpc.ConnectParams{Backoff: policy}),
		grpc.WithContextDialer(dialer.ContextDialer), grpc.WithReturnConnectionError(),
		grpc.WithChainUnaryInterceptor(observeAcceptanceRPC),
	})}
}
func observeAcceptanceRPC(ctx context.Context, method string, request, response any, conn *grpc.ClientConn, invoke grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	var id string
	switch method {
	case "/containerd.services.containers.v1.Containers/Create":
		if value, ok := request.(*containersapi.CreateContainerRequest); ok && value.Container != nil {
			id = value.Container.ID
		}
	case "/containerd.services.tasks.v1.Tasks/Create":
		if value, ok := request.(*tasksapi.CreateTaskRequest); ok {
			id = value.ContainerID
		}
	case "/containerd.services.tasks.v1.Tasks/Start":
		if value, ok := request.(*tasksapi.StartRequest); ok && value.ExecID == "" {
			id = value.ContainerID
		}
	}
	if id != "" {
		namespace, _ := namespaces.Namespace(ctx)
		acceptanceObservers.RLock()
		observer := acceptanceObservers.callbacks[namespace]
		acceptanceObservers.RUnlock()
		if observer != nil {
			observer(method, id)
		}
	}
	return invoke(ctx, method, request, response, conn, opts...)
}
