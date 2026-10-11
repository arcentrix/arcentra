package taskqueue

import (
	"crypto/sha256"
	"fmt"
)

// TaskTypeJobCancel 标识独立控制队列上的作业取消命令
const TaskTypeJobCancel = "pipeline.job_cancel"

// JobCancelTaskPayload 描述发送给指定 Agent 的取消命令
type JobCancelTaskPayload struct {
	AgentID  string `json:"agentId"`
	JobRunID string `json:"jobRunId"`
	Reason   string `json:"reason,omitempty"`
}

// AgentTopicPrefix 返回稳定且可用于 Kafka 主题的 Agent 专属前缀
func AgentTopicPrefix(agentID string) string {
	return fmt.Sprintf("ARCENTRA_AGENT_%x", sha256.Sum256([]byte(agentID)))
}

// AgentQueue 返回指定 Agent 的普通作业队列
func AgentQueue(agentID string) string { return AgentTopicPrefix(agentID) + "_PRIORITY_NORMAL" }

// AgentControlTopicPrefix 返回独立于作业执行的控制队列前缀
func AgentControlTopicPrefix(agentID string) string { return AgentTopicPrefix(agentID) + "_CONTROL" }

// AgentControlQueue 返回指定 Agent 的取消命令队列
func AgentControlQueue(agentID string) string {
	return AgentControlTopicPrefix(agentID) + "_PRIORITY_NORMAL"
}
