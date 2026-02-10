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

package service

import (
	"context"
	"fmt"
	"time"

	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/websocket/v2"
)

// LogStreamHandler WebSocket日志流处理器
type LogStreamHandler struct {
	logAggregator *LogAggregator
}

// NewLogStreamHandler 创建WebSocket日志流处理器
func NewLogStreamHandler(logAggregator *LogAggregator) *LogStreamHandler {
	return &LogStreamHandler{
		logAggregator: logAggregator,
	}
}

// LogStreamRequest WebSocket请求消息
type LogStreamRequest struct {
	Type      string `json:"type"`        // subscribe, unsubscribe
	StepRunID string `json:"step_run_id"` // 步骤执行ID
	FromLine  int32  `json:"from_line"`   // 从第几行开始
}

// LogStreamResponse WebSocket响应消息
type LogStreamResponse struct {
	Type      string    `json:"type"`        // log, error, complete
	StepRunID string    `json:"step_run_id"` // 步骤执行ID
	Log       *LogEntry `json:"log,omitempty"`
	Error     string    `json:"error,omitempty"`
	Message   string    `json:"message,omitempty"`
}

// Upgrade 创建WebSocket升级中间件
func (h *LogStreamHandler) Upgrade() fiber.Handler {
	return websocket.New(func(conn *websocket.Conn) {
		defer func() {
			if err := conn.Close(); err != nil {
				log.Errorw("failed to close websocket", "error", err)
			}
		}()

		// 心跳ticker
		heartbeatTicker := time.NewTicker(30 * time.Second)
		defer heartbeatTicker.Stop()

		// 上下文和取消函数
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// 活跃订阅
		var activeSubscription <-chan *LogEntry
		var currentStepRunID string

		// 发送消息的辅助函数
		sendResponse := func(resp *LogStreamResponse) error {
			data, err := sonic.Marshal(resp)
			if err != nil {
				log.Errorw("failed to marshal response", "error", err)
				return err
			}
			return conn.WriteMessage(websocket.TextMessage, data)
		}

		// 启动接收消息的goroutine
		messageChan := make(chan *LogStreamRequest, 10)
		safe.Go(func() {
			for {
				_, message, err := conn.ReadMessage()
				if err != nil {
					log.Infow("websocket connection closed", "error", err)
					cancel()
					return
				}

				var req LogStreamRequest
				if err := sonic.Unmarshal(message, &req); err != nil {
					log.Errorw("failed to unmarshal request", "error", err)
					sendResponse(&LogStreamResponse{
						Type:  "error",
						Error: fmt.Sprintf("invalid request format: %v", err),
					})
					continue
				}

				messageChan <- &req
			}
		})

		// 主循环
		for {
			select {
			case <-ctx.Done():
				return

			case <-heartbeatTicker.C:
				// 发送心跳
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					log.Errorw("failed to send ping", "error", err)
					return
				}

			case req := <-messageChan:
				switch req.Type {
				case "subscribe":
					// 处理订阅请求
					if currentStepRunID != "" && currentStepRunID != req.StepRunID {
						// 取消之前的订阅
						cancel()
						activeSubscription = nil
					}

					currentStepRunID = req.StepRunID
					log.Infow("client subscribing to step run", "stepRunId", req.StepRunID, "fromLine", req.FromLine)

					// 先发送历史日志
					safe.Go(func() {
						historicalLogs, err := h.logAggregator.GetLogsByStepRunID(req.StepRunID, req.FromLine, 1000)
						if err != nil {
							log.Errorw("failed to get historical logs", "stepRunId", req.StepRunID, "error", err)
							sendResponse(&LogStreamResponse{
								Type:      "error",
								StepRunID: req.StepRunID,
								Error:     fmt.Sprintf("failed to load history: %v", err),
							})
							return
						}

						// 发送历史日志
						for _, entry := range historicalLogs {
							if err := sendResponse(&LogStreamResponse{
								Type:      "log",
								StepRunID: req.StepRunID,
								Log:       entry,
							}); err != nil {
								log.Errorw("failed to send historical log", "stepRunId", req.StepRunID, "error", err)
								return
							}
						}

						// 发送历史日志完成标记
						sendResponse(&LogStreamResponse{
							Type:      "message",
							StepRunID: req.StepRunID,
							Message:   "historical logs loaded",
						})
					})

					// 订阅实时日志
					activeSubscription = h.logAggregator.Subscribe(ctx, req.StepRunID)

					// 确认订阅成功
					sendResponse(&LogStreamResponse{
						Type:      "message",
						StepRunID: req.StepRunID,
						Message:   "subscribed",
					})

				case "unsubscribe":
					// 取消订阅
					if currentStepRunID == req.StepRunID {
						currentStepRunID = ""
						activeSubscription = nil

						sendResponse(&LogStreamResponse{
							Type:      "message",
							StepRunID: req.StepRunID,
							Message:   "unsubscribed",
						})
					}

				default:
					sendResponse(&LogStreamResponse{
						Type:  "error",
						Error: fmt.Sprintf("unknown request type: %s", req.Type),
					})
				}

			case entry, ok := <-activeSubscription:
				if !ok {
					// channel关闭
					activeSubscription = nil
					if currentStepRunID != "" {
						sendResponse(&LogStreamResponse{
							Type:      "complete",
							StepRunID: currentStepRunID,
							Message:   "log stream completed",
						})
					}
					continue
				}

				// 发送实时日志
				if err := sendResponse(&LogStreamResponse{
					Type:      "log",
					StepRunID: currentStepRunID,
					Log:       entry,
				}); err != nil {
					log.Errorw("failed to send real-time log", "stepRunId", currentStepRunID, "error", err)
					return
				}
			}
		}
	})
}
