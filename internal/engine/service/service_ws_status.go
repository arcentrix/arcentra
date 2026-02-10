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
	"fmt"
	"strings"
	"time"

	"github.com/arcentrix/arcentra/internal/engine/model"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/mq/kafka"
	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/arcentrix/arcentra/pkg/ws"
	"github.com/bytedance/sonic"
)

type statusSubscription struct {
	params WSParams
}

var (
	statusTopic = "EVENT_PIPELINE"
	clientId    = "arcentra-ws-status"
)

func (h *WSHandle) handleStatus(conn ws.Conn, action string, params WSParams) error {
	switch action {
	case actionUnsubscribe:
		h.removeStatusSubscription(conn.ID())
		return h.sendMessage(conn, channelStatus, "unsubscribed", params, nil)
	case actionSubscribe:
	default:
		return h.sendError(conn, channelStatus, params, fmt.Sprintf("unknown action: %s", action))
	}

	stepRun, err := h.getStepRun(params)
	if err != nil {
		return h.sendError(conn, channelStatus, params, fmt.Sprintf("load status failed: %v", err))
	}
	if stepRun == nil {
		return h.sendError(conn, channelStatus, params, "step run not found")
	}

	h.sendMessage(conn, channelStatus, "status_snapshot", params, stepRun)

	if !isRunningStatus(stepRun.Status) {
		return nil
	}

	h.addStatusSubscription(conn.ID(), params)
	h.startStatusConsumer()
	return nil
}

func (h *WSHandle) addStatusSubscription(connID string, params WSParams) {
	h.statusMu.Lock()
	defer h.statusMu.Unlock()
	h.statusSubs[connID] = &statusSubscription{params: params}
}

func (h *WSHandle) removeStatusSubscription(connID string) {
	h.statusMu.Lock()
	defer h.statusMu.Unlock()
	delete(h.statusSubs, connID)
}

func (h *WSHandle) startStatusConsumer() {
	h.statusConsume.Do(func() {
		safe.Go(func() {
			h.consumeStatusEvents()
		})
	})
}

func (h *WSHandle) consumeStatusEvents() {
	if h.kafkaCfg.BootstrapServers == "" {
		return
	}

	clientOptions := []kafka.ClientOption{
		kafka.WithSecurityProtocol(h.kafkaCfg.SecurityProtocol),
		kafka.WithSaslMechanism(h.kafkaCfg.Sasl.Mechanism),
		kafka.WithSaslUsername(h.kafkaCfg.Sasl.Username),
		kafka.WithSaslPassword(h.kafkaCfg.Sasl.Password),
		kafka.WithSslCaFile(h.kafkaCfg.Ssl.CaFile),
		kafka.WithSslCertFile(h.kafkaCfg.Ssl.CertFile),
		kafka.WithSslKeyFile(h.kafkaCfg.Ssl.KeyFile),
		kafka.WithSslPassword(h.kafkaCfg.Ssl.Password),
	}

	consumer, err := kafka.NewConsumer(
		h.kafkaCfg.BootstrapServers,
		statusTopic,
		clientId,
		kafka.WithConsumerClientOptions(clientOptions...),
		kafka.WithConsumerAutoOffsetReset("earliest"),
	)
	if err != nil {
		log.Warnw("failed to create kafka status consumer", "error", err)
		return
	}
	defer consumer.Close()

	if err := consumer.Subscribe([]string{statusTopic}); err != nil {
		log.Warnw("failed to subscribe status topic", "error", err)
		return
	}

	for {
		select {
		case <-h.statusStop:
			return
		default:
		}

		msg, err := consumer.ReadMessage(200 * time.Millisecond)
		if err != nil {
			continue
		}

		var event map[string]any
		if err := sonic.Unmarshal(msg.Value, &event); err != nil {
			continue
		}

		h.dispatchStatusEvent(event)
	}
}

func (h *WSHandle) dispatchStatusEvent(event map[string]any) {
	info := parseEventInfo(event)
	if info.pipelineId == "" {
		return
	}

	h.statusMu.RLock()
	defer h.statusMu.RUnlock()
	for connID, sub := range h.statusSubs {
		if !matchStatusSubscription(sub.params, info) {
			continue
		}
		conn, ok := h.hub.GetConn(connID)
		if !ok {
			continue
		}
		payload := map[string]any{
			"pipelineId": info.pipelineId,
			"jobId":      info.jobId,
			"stepRunId":  info.stepRunId,
			"eventType":  info.eventType,
			"status":     info.status,
			"subject":    info.subject,
			"raw":        event,
		}
		_ = h.sendMessage(conn, channelStatus, "status", sub.params, payload)
	}
}

func (h *WSHandle) getStepRun(params WSParams) (*model.StepRun, error) {
	if h.stepRunRepo == nil {
		return nil, fmt.Errorf("step run repository is not available")
	}
	return h.stepRunRepo.GetStepRun(params.PipelineId, params.JobId, params.StepRunId)
}

func isRunningStatus(status int) bool {
	switch status {
	case 1, 2, 3:
		return true
	default:
		return false
	}
}

type eventInfo struct {
	eventType  string
	subject    string
	status     string
	pipelineId string
	jobName    string
	stepName   string
	jobId      string
	stepRunId  string
}

func parseEventInfo(event map[string]any) eventInfo {
	info := eventInfo{}
	if value, ok := event["type"].(string); ok {
		info.eventType = value
	}
	if value, ok := event["subject"].(string); ok {
		info.subject = value
	}

	if value, ok := event["pipelineId"].(string); ok {
		info.pipelineId = value
	}
	if info.pipelineId == "" {
		if value, ok := event["pipelineNamespace"].(string); ok {
			info.pipelineId = value
		}
	}
	if value, ok := event["stepId"].(string); ok {
		info.stepName = value
	}

	if data, ok := event["data"].(map[string]any); ok {
		if status, ok := data["status"].(string); ok {
			info.status = status
		}
	}

	if info.subject != "" {
		pipelineId, jobName, stepName := parseSubject(info.subject)
		if info.pipelineId == "" {
			info.pipelineId = pipelineId
		}
		if info.jobName == "" {
			info.jobName = jobName
		}
		if info.stepName == "" {
			info.stepName = stepName
		}
	}

	if info.pipelineId != "" && info.jobName != "" {
		info.jobId = info.pipelineId + "-" + info.jobName
	}
	if info.jobId != "" && info.stepName != "" {
		info.stepRunId = info.jobId + "-" + info.stepName
	}

	return info
}

func parseSubject(subject string) (pipelineId, jobName, stepName string) {
	if strings.HasPrefix(subject, "pipeline/") {
		parts := strings.Split(subject, "/")
		if len(parts) >= 2 {
			pipelineId = parts[1]
		}
		if len(parts) >= 4 && parts[2] == "step" {
			stepName = parts[3]
		}
		return
	}

	if !strings.HasPrefix(subject, "pipeline:") {
		return
	}

	parts := strings.Split(subject, ":")
	for i := 0; i < len(parts)-1; i++ {
		switch parts[i] {
		case "pipeline":
			pipelineId = parts[i+1]
		case "job":
			jobName = parts[i+1]
		case "step":
			stepName = parts[i+1]
		}
	}
	return
}

func matchStatusSubscription(params WSParams, info eventInfo) bool {
	if params.PipelineId != "" && info.pipelineId != "" && params.PipelineId != info.pipelineId {
		return false
	}

	jobName := deriveJobName(params.PipelineId, params.JobId)
	stepName := deriveStepName(params.JobId, params.StepRunId)

	if info.jobName != "" && jobName != "" && info.jobName != jobName {
		return false
	}
	if info.stepName != "" && stepName != "" && info.stepName != stepName {
		return false
	}
	return true
}

func deriveJobName(pipelineId, jobId string) string {
	prefix := pipelineId + "-"
	if strings.HasPrefix(jobId, prefix) {
		return strings.TrimPrefix(jobId, prefix)
	}
	return jobId
}

func deriveStepName(jobId, stepRunId string) string {
	prefix := jobId + "-"
	if strings.HasPrefix(stepRunId, prefix) {
		return strings.TrimPrefix(stepRunId, prefix)
	}
	return stepRunId
}
