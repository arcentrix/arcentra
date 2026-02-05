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

package grpc

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"strings"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	steprunv1 "github.com/arcentrix/arcentra/api/steprun/v1"
	streamv1 "github.com/arcentrix/arcentra/api/stream/v1"
	"github.com/arcentrix/arcentra/internal/pkg/grpc/interceptor"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/trace/inject"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

type ClientConf struct {
	ServerAddr           string // server address (host:port format, e.g., "localhost:9090")
	Token                string // Bearer token for authentication
	ReadWriteTimeout     int    // read write timeout (seconds)
	MaxMsgSize           int    // max message size (bytes), 0 means use default value
	MaxReconnectAttempts int    // max reconnection attempts, 0 means unlimited
}

// ClientWrapper gRPC client wrapper
type ClientWrapper struct {
	conn   *grpc.ClientConn
	config ClientConf

	// service clients
	AgentClient    agentv1.AgentServiceClient
	StepRunClient  steprunv1.StepRunServiceClient
	StreamClient   streamv1.StreamServiceClient
	PipelineClient pipelinev1.PipelineServiceClient

	// reconnection management
	mu                  sync.RWMutex
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

	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
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
		conn:          conn,
		config:        cfg,
		stopReconnect: make(chan struct{}),
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
}

// GetConn get connection for advanced usage
func (c *ClientWrapper) GetConn() *grpc.ClientConn {
	return c.conn
}

// Start gRPC client and monitor connection state
func (c *ClientWrapper) Start(cfg ClientConf) error {
	c.config = cfg
	log.Infow("gRPC client started", "address", cfg.ServerAddr)

	// Start connection state monitoring
	go c.monitorConnection()

	// wait for exit signal
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch

	log.Info("Shutting down gRPC client...")
	return c.Close()
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
			log.Errorw("Max reconnection attempts reached, stopping reconnection", "max_attempts", maxAttempts, "current_attempts", c.reconnectAttempts)
		}
		return fmt.Errorf("max reconnection attempts (%d) reached", maxAttempts)
	}

	c.reconnecting = true
	defer func() {
		c.reconnecting = false
	}()

	c.reconnectAttempts++
	log.Infow("Attempting to reconnect", "attempt", c.reconnectAttempts, "max_attempts", maxAttempts)

	// Close old connection if exists
	if c.conn != nil {
		err := c.conn.Close()
		if err != nil {
			return err
		}
	}

	// Recreate connection
	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithChainUnaryInterceptor(
			inject.UnaryClientInterceptor(), // OpenTelemetry trace interceptor
			interceptor.LoggingUnaryClientInterceptor(),
		),
		grpc.WithChainStreamInterceptor(
			inject.StreamClientInterceptor(), // OpenTelemetry trace interceptor
			interceptor.LoggingStreamClientInterceptor(),
		),
	}

	// set max message size (receive and send)
	if c.config.MaxMsgSize > 0 {
		opts = append(opts,
			grpc.WithDefaultCallOptions(
				grpc.MaxCallRecvMsgSize(c.config.MaxMsgSize),
				grpc.MaxCallSendMsgSize(c.config.MaxMsgSize),
			),
		)
	}

	// Ensure address has host
	serverAddr := c.config.ServerAddr
	if strings.HasPrefix(serverAddr, ":") {
		serverAddr = "localhost" + serverAddr
	}

	conn, err := grpc.NewClient(serverAddr, opts...)
	if err != nil {
		return fmt.Errorf("failed to recreate gRPC client: %w", err)
	}

	c.conn = conn
	c.initClients()

	// Reset reconnect attempts on successful reconnection
	c.reconnectAttempts = 0
	c.maxReconnectReached = false

	return nil
}

// Close client connection
func (c *ClientWrapper) Close() error {
	// Stop reconnection monitoring
	close(c.stopReconnect)

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// WithAuthContext create context with auth
func (c *ClientWrapper) WithAuthContext(ctx context.Context) context.Context {
	if c.config.Token == "" {
		return ctx
	}
	md := metadata.New(map[string]string{
		"authorization": fmt.Sprintf("bearer %s", c.config.Token),
	})
	return metadata.NewOutgoingContext(ctx, md)
}

// WithTimeout create context with timeout (use default timeout if not configured)
// return context and cancel function, caller should ensure calling cancel
func (c *ClientWrapper) WithTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := time.Duration(c.config.ReadWriteTimeout) * time.Second
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
