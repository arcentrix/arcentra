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
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/arcentrix/arcentra/internal/engine/repo"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/mq/kafka"
	"github.com/arcentrix/arcentra/pkg/ws"
	"github.com/bytedance/sonic"
	fws "github.com/fasthttp/websocket"
)

const (
	channelLog    = "channel_log"
	channelStatus = "channel_status"

	actionSubscribe   = "subscribe"
	actionUnsubscribe = "unsubscribe"
)

type WSParams struct {
	PipelineId string `json:"pipelineId"`
	JobId      string `json:"jobId"`
	StepRunId  string `json:"stepRunId"`
}

type WSRequest struct {
	Channel string   `json:"channel"`
	Action  string   `json:"action,omitempty"`
	Params  WSParams `json:"params"`
}

type WSResponse struct {
	Channel string    `json:"channel"`
	Type    string    `json:"type"`
	Params  *WSParams `json:"params,omitempty"`
	Data    any       `json:"data,omitempty"`
	Error   string    `json:"error,omitempty"`
	Message string    `json:"message,omitempty"`
}

type WSHandle struct {
	hub         ws.Hub
	logAgg      *LogAggregator
	stepRunRepo repo.IStepRunRepository
	kafkaCfg    kafka.Config

	logMu   sync.Mutex
	logSubs map[string]*logSubscription

	statusMu      sync.RWMutex
	statusSubs    map[string]*statusSubscription
	statusConsume sync.Once
	statusStop    chan struct{}
}

func NewWSHandle(hub ws.Hub, logAgg *LogAggregator, stepRunRepo repo.IStepRunRepository, kafkaCfg kafka.Config) *WSHandle {
	return &WSHandle{
		hub:         hub,
		logAgg:      logAgg,
		stepRunRepo: stepRunRepo,
		kafkaCfg:    kafkaCfg,
		logSubs:     make(map[string]*logSubscription),
		statusSubs:  make(map[string]*statusSubscription),
		statusStop:  make(chan struct{}),
	}
}

func (h *WSHandle) OnConnect(conn ws.Conn) error {
	return nil
}

func (h *WSHandle) OnMessage(conn ws.Conn, messageType int, data []byte) error {
	if messageType != ws.TextMessage && messageType != ws.BinaryMessage {
		return nil
	}

	var req WSRequest
	if err := sonic.Unmarshal(data, &req); err != nil {
		return h.sendInvalidRequest(conn)
	}

	if err := validateParams(req.Params); err != nil {
		return h.sendError(conn, req.Channel, req.Params, err.Error())
	}

	action := strings.ToLower(strings.TrimSpace(req.Action))
	if action == "" {
		action = actionSubscribe
	}

	switch req.Channel {
	case channelLog:
		return h.handleLog(conn, action, req.Params)
	case channelStatus:
		return h.handleStatus(conn, action, req.Params)
	default:
		return h.sendError(conn, req.Channel, req.Params, fmt.Sprintf("unknown channel: %s", req.Channel))
	}
}

func (h *WSHandle) OnDisconnect(conn ws.Conn, err error) {
	h.cancelLogSubscription(conn.ID())
	h.removeStatusSubscription(conn.ID())

	// 客户端主动断开通常会触发 CloseNormalClosure(1000) / CloseGoingAway(1001)
	if err != nil && fws.IsCloseError(err, fws.CloseNormalClosure, fws.CloseGoingAway) {
		var ce *fws.CloseError
		if errors.As(err, &ce) {
			log.Infow("ws client disconnected", "conn", conn.ID(), "remote", conn.RemoteAddr(), "code", ce.Code, "text", ce.Text)
			return
		}
		log.Infow("ws client disconnected", "conn", conn.ID(), "remote", conn.RemoteAddr())
	}
}

func (h *WSHandle) OnError(conn ws.Conn, err error) {
	if err != nil {
		log.Warnw("ws handler error", "conn", conn.ID(), "error", err)
	}
}

func (h *WSHandle) sendError(conn ws.Conn, channel string, params WSParams, msg string) error {
	return conn.WriteJSON(WSResponse{
		Channel: channel,
		Type:    "error",
		Params:  paramsOrNil(params),
		Error:   msg,
	})
}

func (h *WSHandle) sendInvalidRequest(conn ws.Conn) error {
	userMsg := "invalid request: message must be a JSON object"

	return conn.WriteJSON(WSResponse{
		Channel: "",
		Type:    "error",
		Params:  nil,
		Error:   userMsg,
		Message: "",
	})
}

func (h *WSHandle) sendMessage(conn ws.Conn, channel, messageType string, params WSParams, data any) error {
	return conn.WriteJSON(WSResponse{
		Channel: channel,
		Type:    messageType,
		Params:  paramsOrNil(params),
		Data:    data,
	})
}

func paramsOrNil(p WSParams) *WSParams {
	if p.PipelineId == "" && p.JobId == "" && p.StepRunId == "" {
		return nil
	}
	return &p
}

func validateParams(params WSParams) error {
	if params.PipelineId == "" || params.JobId == "" || params.StepRunId == "" {
		return fmt.Errorf("pipelineId, jobId and stepRunId are required")
	}
	return nil
}
