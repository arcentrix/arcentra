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

	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/arcentrix/arcentra/pkg/ws"
)

const logHistoryChunkSize = 200

type logSubscription struct {
	params WSParams
	cancel context.CancelFunc
	ch     <-chan *LogEntry
}

func (h *WSHandle) handleLog(conn ws.Conn, action string, params WSParams) error {
	switch action {
	case actionUnsubscribe:
		h.cancelLogSubscription(conn.ID())
		return h.sendMessage(conn, channelLog, "unsubscribed", params, nil)
	case actionSubscribe:
	default:
		return h.sendError(conn, channelLog, params, fmt.Sprintf("unknown action: %s", action))
	}

	if h.logAgg == nil {
		return h.sendError(conn, channelLog, params, "log aggregator is not available")
	}

	h.cancelLogSubscription(conn.ID())

	ctx, cancel := context.WithCancel(context.Background())
	sub := &logSubscription{
		params: params,
		cancel: cancel,
		ch:     h.logAgg.Subscribe(ctx, params.StepRunId),
	}
	h.logMu.Lock()
	h.logSubs[conn.ID()] = sub
	h.logMu.Unlock()

	_ = h.sendMessage(conn, channelLog, "subscribed", params, nil)
	safe.Go(func() {
		h.sendLogHistory(conn, params)
	})
	safe.Go(func() {
		h.streamRealtimeLogs(conn, sub)
	})
	return nil
}

func (h *WSHandle) sendLogHistory(conn ws.Conn, params WSParams) {
	fromLine := int32(0)
	for {
		logs, err := h.logAgg.GetLogsByStepRunID(params.StepRunId, fromLine, logHistoryChunkSize)
		if err != nil {
			_ = h.sendError(conn, channelLog, params, fmt.Sprintf("load history failed: %v", err))
			return
		}
		if len(logs) == 0 {
			break
		}
		_ = h.sendMessage(conn, channelLog, "log_chunk", params, logs)
		fromLine = logs[len(logs)-1].LineNumber + 1
		if len(logs) < logHistoryChunkSize {
			break
		}
	}
	_ = h.sendMessage(conn, channelLog, "history_done", params, nil)
}

func (h *WSHandle) streamRealtimeLogs(conn ws.Conn, sub *logSubscription) {
	for entry := range sub.ch {
		if err := h.sendMessage(conn, channelLog, "log", sub.params, entry); err != nil {
			h.cancelLogSubscription(conn.ID())
			return
		}
	}
}

func (h *WSHandle) cancelLogSubscription(connID string) {
	h.logMu.Lock()
	defer h.logMu.Unlock()
	if sub, ok := h.logSubs[connID]; ok {
		sub.cancel()
		delete(h.logSubs, connID)
	}
}
