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
	"maps"
	"sync"
)

// DefaultHub 默认的连接管理器实现
type DefaultHub struct {
	// conns 存储所有连接
	conns map[string]Conn

	// mu 保护 conns 的并发访问
	mu sync.RWMutex

	// broadcast 广播消息通道
	broadcast chan *broadcastMessage

	// register 注册连接通道
	register chan Conn

	// unregister 注销连接通道
	unregister chan Conn
}

type broadcastMessage struct {
	messageType int
	data        []byte
	excludeID   string // 排除的连接 ID，为空则广播给所有连接
}

// NewHub 创建一个新的连接管理器
func NewHub() Hub {
	hub := &DefaultHub{
		conns:      make(map[string]Conn),
		broadcast:  make(chan *broadcastMessage, 256),
		register:   make(chan Conn),
		unregister: make(chan Conn),
	}

	// 启动消息处理 goroutine
	go hub.run()

	return hub
}

// run 运行连接管理器的主循环
func (h *DefaultHub) run() {
	for {
		select {
		case conn := <-h.register:
			h.mu.Lock()
			h.conns[conn.ID()] = conn
			h.mu.Unlock()

		case conn := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.conns[conn.ID()]; ok {
				delete(h.conns, conn.ID())
				conn.Close()
			}
			h.mu.Unlock()

		case message := <-h.broadcast:
			h.mu.RLock()
			for id, conn := range h.conns {
				if message.excludeID != "" && id == message.excludeID {
					continue
				}
				// 异步发送，避免阻塞
				go func(c Conn) {
					_ = c.WriteMessage(message.messageType, message.data)
				}(conn)
			}
			h.mu.RUnlock()
		}
	}
}

// Register 注册一个新连接
func (h *DefaultHub) Register(conn Conn) {
	h.register <- conn
}

// Unregister 注销一个连接
func (h *DefaultHub) Unregister(conn Conn) {
	h.unregister <- conn
}

// Broadcast 向所有连接广播消息
func (h *DefaultHub) Broadcast(messageType int, data []byte) {
	h.broadcast <- &broadcastMessage{
		messageType: messageType,
		data:        data,
	}
}

// BroadcastJSON 向所有连接广播 JSON 消息
func (h *DefaultHub) BroadcastJSON(v any) {
	// 这个方法需要在具体实现中序列化 JSON
	// 由于 Conn 接口已经提供了 WriteJSON，这里先留空
	// 实际使用时应该调用每个连接的 WriteJSON
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, conn := range h.conns {
		go func(c Conn) {
			_ = c.WriteJSON(v)
		}(conn)
	}
}

// SendToID 向指定 ID 的连接发送消息
func (h *DefaultHub) SendToID(id string, messageType int, data []byte) error {
	h.mu.RLock()
	conn, ok := h.conns[id]
	h.mu.RUnlock()

	if !ok {
		return ErrConnNotFound
	}

	return conn.WriteMessage(messageType, data)
}

// SendToIDJSON 向指定 ID 的连接发送 JSON 消息
func (h *DefaultHub) SendToIDJSON(id string, v any) error {
	h.mu.RLock()
	conn, ok := h.conns[id]
	h.mu.RUnlock()

	if !ok {
		return ErrConnNotFound
	}

	return conn.WriteJSON(v)
}

// GetConn 获取指定 ID 的连接
func (h *DefaultHub) GetConn(id string) (Conn, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	conn, ok := h.conns[id]
	return conn, ok
}

// GetConns 获取所有连接
func (h *DefaultHub) GetConns() map[string]Conn {
	h.mu.RLock()
	defer h.mu.RUnlock()

	// 返回一个副本，避免外部修改
	conns := make(map[string]Conn, len(h.conns))
	maps.Copy(conns, h.conns)
	return conns
}

// Count 返回当前连接数
func (h *DefaultHub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.conns)
}
