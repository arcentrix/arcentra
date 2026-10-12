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

package grpc

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	gatewayv1 "github.com/arcentrix/arcentra/api/gateway/v1"
	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	steprunv1 "github.com/arcentrix/arcentra/api/steprun/v1"
	streamv1 "github.com/arcentrix/arcentra/api/stream/v1"
	"github.com/arcentrix/arcentra/internal/shared/grpc/interceptor"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/arcentrix/arcentra/pkg/trace/inject"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/metadata"
)

type ClientConf struct {
	TLS                  ClientTLSConfig // 服务器证书认证，仅由操作者显式启用
	ServerAddr           string          // server address (host:port format, e.g., "localhost:9090")
	Token                string          // Bearer token for authentication
	ReadWriteTimeout     int             // read write timeout (seconds)
	MaxMsgSize           int             // max message size (bytes), 0 means use default value
	MaxReconnectAttempts int             // max reconnection attempts, 0 means unlimited
}

// ClientWrapper gRPC client wrapper
type ClientWrapper struct {
	conn                    *grpc.ClientConn
	config                  ClientConf
	controllerAuthenticated bool
	controllerTrustDigest   string

	// service clients
	AgentClient    agentv1.AgentServiceClient
	StepRunClient  steprunv1.StepRunServiceClient
	StreamClient   streamv1.StreamServiceClient
	PipelineClient pipelinev1.PipelineServiceClient
	GatewayClient  gatewayv1.GatewayServiceClient

	// reconnection management
	mu                  sync.RWMutex
	stopOnce            sync.Once
	stopReconnect       chan struct{}
	reconnecting        bool
	reconnectAttempts   int  // current reconnection attempt count
	maxReconnectReached bool // flag to indicate max attempts reached
}

// NewGrpcClient create gRPC client
func NewGrpcClient(cfg ClientConf) (*ClientWrapper, error) {
	if cfg.ServerAddr == "" {
		return nil, fmt.Errorf("serverAddr is required")
	}

	transport, trustDigest, err := clientTransportCredentials(context.Background(), cfg.TLS)
	if err != nil {
		return nil, err
	}
	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(transport),
		grpc.WithChainUnaryInterceptor(
			inject.UnaryClientInterceptor(), // OpenTelemetry trace interceptor
			//	interceptor.AuthUnaryClientInterceptor(cfg.Token),
			interceptor.LoggingUnaryClientInterceptor(),
		),
		grpc.WithChainStreamInterceptor(
			inject.StreamClientInterceptor(), // OpenTelemetry trace interceptor
			//	interceptor.AuthStreamClientInterceptor(cfg.Token),
			interceptor.LoggingStreamClientInterceptor(),
		),
	}

	// set max message size (receive and send)
	if cfg.MaxMsgSize > 0 {
		opts = append(opts,
			grpc.WithDefaultCallOptions(
				grpc.MaxCallRecvMsgSize(cfg.MaxMsgSize),
				grpc.MaxCallSendMsgSize(cfg.MaxMsgSize),
			),
		)
	}

	// Validate server address
	if cfg.ServerAddr == "" {
		return nil, fmt.Errorf("serverAddr is required")
	}
	// Ensure address has host (if only port, use localhost)
	if strings.HasPrefix(cfg.ServerAddr, ":") {
		cfg.ServerAddr = "localhost" + cfg.ServerAddr
		log.Warnw("serverAddr missing host, using localhost", "address", cfg.ServerAddr)
	}

	conn, err := grpc.NewClient(cfg.ServerAddr, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create gRPC client: %w", err)
	}

	client := &ClientWrapper{
		conn:                    conn,
		config:                  cfg,
		controllerAuthenticated: cfg.TLS.Enabled,
		controllerTrustDigest:   trustDigest,
		stopReconnect:           make(chan struct{}),
	}
	client.initClients()

	log.Infow("gRPC client created", "address", cfg.ServerAddr)

	return client, nil
}

// initClients init clients
func (c *ClientWrapper) initClients() {
	c.AgentClient = agentv1.NewAgentServiceClient(c.conn)
	c.StepRunClient = steprunv1.NewStepRunServiceClient(c.conn)
	c.StreamClient = streamv1.NewStreamServiceClient(c.conn)
	c.PipelineClient = pipelinev1.NewPipelineServiceClient(c.conn)
	c.GatewayClient = gatewayv1.NewGatewayServiceClient(c.conn)
}

// GetConn get connection for advanced usage
func (c *ClientWrapper) GetConn() *grpc.ClientConn {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.conn
}

// Start gRPC client and monitor connection state
func (c *ClientWrapper) Start(cfg ClientConf) error {
	c.mu.Lock()
	if normalizeClientAddress(cfg.ServerAddr) != normalizeClientAddress(c.config.ServerAddr) || cfg.TLS != c.config.TLS {
		c.mu.Unlock()
		return fmt.Errorf("gRPC transport configuration cannot change without a new client")
	}
	if c.config.Token != "" {
		cfg.Token = c.config.Token
	}
	c.config = cfg
	c.mu.Unlock()
	log.Infow("gRPC client started", "address", cfg.ServerAddr)

	// Start connection state monitoring
	safe.Go(c.monitorConnection)

	// 生命周期由 Bootstrap 管理，避免收到信号时先关闭连接而丢失终态报告
	<-c.stopReconnect
	return nil
}

// monitorConnection monitors connection state and handles reconnection
func (c *ClientWrapper) monitorConnection() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.stopReconnect:
			return
		case <-ticker.C:
			c.mu.RLock()
			conn := c.conn
			c.mu.RUnlock()

			if conn == nil {
				continue
			}

			state := conn.GetState()

			switch state {
			case connectivity.TransientFailure:
				// Connection failed, try to reconnect
				log.Warn("gRPC connection transient failure, attempting to reconnect")
				if err := c.reconnect(); err != nil {
					log.Errorw("Failed to reconnect", "error", err)
				}
			case connectivity.Shutdown:
				// Connection is closed, try to reconnect
				log.Warn("gRPC connection shutdown, attempting to reconnect")
				if err := c.reconnect(); err != nil {
					log.Errorw("Failed to reconnect", "error", err)
				}
			case connectivity.Connecting:
				// Connection is connecting, wait for it to complete
				log.Debug("gRPC connection connecting, waiting...")
			case connectivity.Ready, connectivity.Idle:
				// Connection is healthy, reset reconnect attempts
				c.mu.Lock()
				if c.reconnectAttempts > 0 {
					log.Infow("gRPC connection restored, resetting reconnect attempts", "previous_attempts", c.reconnectAttempts)
					c.reconnectAttempts = 0
					c.maxReconnectReached = false
				}
				c.mu.Unlock()
			}
		}
	}
}

// reconnect attempts to reconnect the gRPC client
func (c *ClientWrapper) reconnect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.reconnecting {
		return nil // Already reconnecting
	}

	// Check if max reconnect attempts reached
	maxAttempts := c.config.MaxReconnectAttempts
	if maxAttempts > 0 && c.reconnectAttempts >= maxAttempts {
		if !c.maxReconnectReached {
			c.maxReconnectReached = true
			log.Errorw("Max reconnection attempts reached, stopping reconnection",
				"max_attempts", maxAttempts, "current_attempts", c.reconnectAttempts)
		}
		return fmt.Errorf("max reconnection attempts (%d) reached", maxAttempts)
	}

	c.reconnecting = true
	defer func() {
		c.reconnecting = false
	}()

	c.reconnectAttempts++
	log.Infow("Attempting to reconnect", "attempt", c.reconnectAttempts, "max_attempts", maxAttempts)

	// ClientConn 自行管理连接恢复；不能并发替换已发布的服务客户端指针
	// 已主动关闭的连接不得通过后台监控自动复活
	if c.conn == nil || c.conn.GetState() == connectivity.Shutdown {
		return fmt.Errorf("closed gRPC connection cannot reconnect")
	}
	c.conn.Connect()

	return nil
}

// Close client connection
func (c *ClientWrapper) Close() error {
	// Stop reconnection monitoring
	c.stopOnce.Do(func() {
		if c.stopReconnect != nil {
			close(c.stopReconnect)
		}
	})

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		err := c.conn.Close()
		c.conn = nil
		return err
	}
	return nil
}

// UpdateToken 更新动态注册后使用的认证令牌
func (c *ClientWrapper) UpdateToken(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.config.Token = token
}

// WithAuthContext create context with auth
func (c *ClientWrapper) WithAuthContext(ctx context.Context) context.Context {
	c.mu.RLock()
	token := c.config.Token
	c.mu.RUnlock()
	if token == "" {
		return ctx
	}
	existing, _ := metadata.FromOutgoingContext(ctx)
	md := existing.Copy()
	md.Set("authorization", "bearer "+token)
	return metadata.NewOutgoingContext(ctx, md)
}

// WithTimeout create context with timeout (use default timeout if not configured)
// return context and cancel function, caller should ensure calling cancel
func (c *ClientWrapper) WithTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	c.mu.RLock()
	timeout := time.Duration(c.config.ReadWriteTimeout) * time.Second
	c.mu.RUnlock()
	if timeout <= 0 {
		// use default 30 seconds if not configured
		timeout = 30 * time.Second
	}
	return context.WithTimeout(ctx, timeout)
}

// WithTimeoutAndAuth create context with timeout and auth
// return context and cancel function, caller should ensure calling cancel
func (c *ClientWrapper) WithTimeoutAndAuth(ctx context.Context) (context.Context, context.CancelFunc) {
	timeoutCtx, cancel := c.WithTimeout(ctx)
	return c.WithAuthContext(timeoutCtx), cancel
}
