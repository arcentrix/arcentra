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

package ws

import (
	"context"
)

// Conn 表示一个 WebSocket 连接
type Conn interface {
	// ID 返回连接的唯一标识符
	ID() string

	// ReadMessage 读取一条消息
	ReadMessage() (messageType int, p []byte, err error)

	// WriteMessage 写入一条消息
	WriteMessage(messageType int, data []byte) error

	// WriteJSON 写入 JSON 消息
	WriteJSON(v any) error

	// ReadJSON 读取 JSON 消息
	ReadJSON(v any) error

	// Close 关闭连接
	Close() error

	// RemoteAddr 返回远程地址
	RemoteAddr() string

	// Context 返回连接的上下文
	Context() context.Context

	// SetContext 设置连接的上下文
	SetContext(ctx context.Context)
}

// Hub 管理所有 WebSocket 连接
type Hub interface {
	// Register 注册一个新连接
	Register(conn Conn)

	// Unregister 注销一个连接
	Unregister(conn Conn)

	// Broadcast 向所有连接广播消息
	Broadcast(messageType int, data []byte)

	// BroadcastJSON 向所有连接广播 JSON 消息
	BroadcastJSON(v any)

	// SendToID 向指定 ID 的连接发送消息
	SendToID(id string, messageType int, data []byte) error

	// SendToIDJSON 向指定 ID 的连接发送 JSON 消息
	SendToIDJSON(id string, v any) error

	// GetConn 获取指定 ID 的连接
	GetConn(id string) (Conn, bool)

	// GetConns 获取所有连接
	GetConns() map[string]Conn

	// Count 返回当前连接数
	Count() int
}

// Handler 处理 WebSocket 连接的生命周期事件
type Handler interface {
	// OnConnect 当连接建立时调用
	OnConnect(conn Conn) error

	// OnMessage 当收到消息时调用
	OnMessage(conn Conn, messageType int, data []byte) error

	// OnDisconnect 当连接断开时调用
	OnDisconnect(conn Conn, err error)

	// OnError 当发生错误时调用
	OnError(conn Conn, err error)
}

// MessageType WebSocket 消息类型常量
const (
	// TextMessage 文本消息
	TextMessage = 1
	// BinaryMessage 二进制消息
	BinaryMessage = 2
	// CloseMessage 关闭消息
	CloseMessage = 8
	// PingMessage ping 消息
	PingMessage = 9
	// PongMessage pong 消息
	PongMessage = 10
)
