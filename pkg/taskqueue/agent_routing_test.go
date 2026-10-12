package taskqueue

import (
	"strings"
	"testing"
)

func TestAgentRoutingIsolation(t *testing.T) {
	for _, agent := range []string{"agent-1", "agent/with unsafe characters", ""} {
		work, control := AgentQueue(agent), AgentControlQueue(agent)
		if work == control || !strings.HasPrefix(work, AgentTopicPrefix(agent)) {
			t.Fatal("work and control queues must be isolated")
		}
		if strings.ContainsAny(work, " /:") {
			t.Fatal("unsafe Kafka topic")
		}
		if work != AgentQueue(agent) {
			t.Fatal("routing must be deterministic")
		}
	}
	if AgentQueue("one") == AgentQueue("two") {
		t.Fatal("agents must not consume each other's jobs")
	}
}
