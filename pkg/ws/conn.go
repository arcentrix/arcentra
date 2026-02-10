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

package ws

import (
	"context"
	"sync"
	"time"

	"github.com/arcentrix/arcentra/pkg/id"
	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/websocket/v2"
)

// conn WebSocket 连接实现
type conn struct {
	*websocket.Conn
	id        string
	ctx       context.Context
	ctxMu     sync.RWMutex
	writeMu   sync.Mutex
	hub       Hub
	handler   Handler
	closeOnce sync.Once
	closed    chan struct{}
}

const (
	readlimit  = 1024 * 1024 * 10    // 10MB
	pongWait   = 5 * time.Minute     // 等待 pong 响应的超时时间
	pingPeriod = (pongWait * 9) / 10 // ping 发送周期，应该小于 pongWait
	writeWait  = 10 * time.Second    // 写入超时时间
)

// newConn 创建一个新的 WebSocket 连接
func newConn(wsConn *websocket.Conn, hub Hub, handler Handler) *conn {
	return &conn{
		Conn:    wsConn,
		id:      id.GetUUID(),
		ctx:     context.Background(),
		hub:     hub,
		handler: handler,
		closed:  make(chan struct{}),
	}
}

// ID 返回连接的唯一标识符
func (c *conn) ID() string {
	return c.id
}

// ReadMessage 读取一条消息
func (c *conn) ReadMessage() (messageType int, p []byte, err error) {
	return c.Conn.ReadMessage()
}

// WriteMessage 写入一条消息
func (c *conn) WriteMessage(messageType int, data []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	// 每次写入都设置写超时，避免沿用过期的 deadline 导致 i/o timeout
	_ = c.Conn.SetWriteDeadline(time.Now().Add(writeWait))
	defer func() {
		// 复位，避免影响后续写入
		_ = c.Conn.SetWriteDeadline(time.Time{})
	}()
	return c.Conn.WriteMessage(messageType, data)
}

// WriteJSON 写入 JSON 消息
func (c *conn) WriteJSON(v any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	// 每次写入都设置写超时，避免沿用过期的 deadline 导致 i/o timeout
	_ = c.Conn.SetWriteDeadline(time.Now().Add(writeWait))
	defer func() {
		_ = c.Conn.SetWriteDeadline(time.Time{})
	}()
	return c.Conn.WriteJSON(v)
}

// ReadJSON 读取 JSON 消息
func (c *conn) ReadJSON(v any) error {
	return c.Conn.ReadJSON(v)
}

// Close 关闭连接
func (c *conn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		close(c.closed)
		err = c.Conn.Close()
	})
	return err
}

// RemoteAddr 返回远程地址
func (c *conn) RemoteAddr() string {
	return c.Conn.RemoteAddr().String()
}

// Context 返回连接的上下文
func (c *conn) Context() context.Context {
	c.ctxMu.RLock()
	defer c.ctxMu.RUnlock()
	return c.ctx
}

// SetContext 设置连接的上下文
func (c *conn) SetContext(ctx context.Context) {
	c.ctxMu.Lock()
	defer c.ctxMu.Unlock()
	c.ctx = ctx
}

// Handle 处理 WebSocket 连接
func Handle(hub Hub, handler Handler) fiber.Handler {
	return websocket.New(func(wsConn *websocket.Conn) {
		conn := newConn(wsConn, hub, handler)

		// 设置读取限制和超时
		wsConn.SetReadLimit(readlimit)
		_ = wsConn.SetReadDeadline(time.Now().Add(pongWait))

		// 设置 pong 处理器，用于更新读取超时
		wsConn.SetPongHandler(func(string) error {
			return wsConn.SetReadDeadline(time.Now().Add(pongWait))
		})

		var once sync.Once
		cleanup := func(err error) {
			once.Do(func() {
				if hub != nil {
					hub.Unregister(conn)
				}
				if handler != nil {
					handler.OnDisconnect(conn, err)
				}
			})
			_ = conn.Close()
		}

		// 注册连接
		if hub != nil {
			hub.Register(conn)
		}

		// 调用连接建立回调
		if handler != nil {
			if err := handler.OnConnect(conn); err != nil {
				handler.OnError(conn, err)
				cleanup(err)
				return
			}
		}
		defer cleanup(nil)

		// 启动心跳 goroutine
		safe.Go(func() {
			conn.pingTicker()
		})

		// 消息处理循环
		for {
			messageType, message, err := conn.ReadMessage()
			if err != nil {
				cleanup(err)
				break
			}

			// 更新读取超时
			_ = wsConn.SetReadDeadline(time.Now().Add(pongWait))

			if handler != nil {
				if err := handler.OnMessage(conn, messageType, message); err != nil {
					handler.OnError(conn, err)
				}
			}
		}
	})
}

// pingTicker 定期发送 ping 消息以保持连接活跃
func (c *conn) pingTicker() {
	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := c.WriteMessage(PingMessage, nil); err != nil {
				return
			}
		case <-c.closed:
			return
		}
	}
}
