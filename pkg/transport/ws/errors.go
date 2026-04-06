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

import "errors"

var (
	// ErrConnNotFound 连接未找到
	ErrConnNotFound = errors.New("websocket connection not found")

	// ErrInvalidMessageType 无效的消息类型
	ErrInvalidMessageType = errors.New("invalid message type")

	// ErrConnectionClosed 连接已关闭
	ErrConnectionClosed = errors.New("websocket connection closed")

	// ErrNotSupported 不支持的操作
	ErrNotSupported = errors.New("operation not supported")
)
