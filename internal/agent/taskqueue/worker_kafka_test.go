package taskqueue

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/agent/config"
	"github.com/arcentrix/arcentra/internal/shared/executor"
	kafkaconfig "github.com/arcentrix/arcentra/pkg/mq/kafka"
	"github.com/arcentrix/arcentra/pkg/nova"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/bytedance/sonic"
	ckafka "github.com/confluentinc/confluent-kafka-go/v2/kafka"
)

func TestKafkaAgentRoutingAndCancellation(t *testing.T) {
	if testing.Short() {
		t.Skip("Kafka wire-protocol integration test")
	}
	cluster, err := ckafka.NewMockCluster(1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cluster.Close)
	prefix := fmt.Sprintf("wire-%d", time.Now().UnixNano())
	agents := []string{prefix + "-a", prefix + "-b"}
	root := t.TempDir()
	for _, id := range agents {
		for _, topicPrefix := range []string{taskpayload.AgentTopicPrefix(id), taskpayload.AgentControlTopicPrefix(id)} {
			for _, suffix := range []string{"_PRIORITY_NORMAL", "_PRIORITY_HIGH", "_PRIORITY_LOW", "_TASKS_DELAY_0"} {
				if err := cluster.CreateTopic(topicPrefix+suffix, 1, 1); err != nil {
					t.Fatal(err)
				}
			}
		}
		conf := &config.AgentConfig{Agent: config.AgentInfo{ID: id, Mode: "baremetal", WorkspaceDir: filepath.Join(root, id), JobTimeout: 60}, MessageQueue: config.MqConfig{Kafka: kafkaconfig.Config{BootstrapServers: cluster.BootstrapServers(), SecurityProtocol: "PLAINTEXT"}}, TaskQueue: nova.TaskQueueConfig{DelaySlotCount: 1, DelaySlotDuration: 3600, SessionTimeout: 6000, MaxPollInterval: 120000}}
		manager := executor.NewExecutorManager()
		manager.Register(executor.NewShellExecutor())
		queue, err := StartWorker(context.Background(), conf, nil, manager)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := queue.Stop(); err != nil {
				t.Error(err)
			}
		})
	}
	// 生产端与消费者通过真实 Kafka 协议交互，MockCluster 不替换队列实现
	producer, err := nova.NewTaskQueue(nova.WithKafka(cluster.BootstrapServers(), nova.WithKafkaAuth("PLAINTEXT", "", "", "")))
	if err != nil {
		t.Fatal(err)
	}
	defer producer.Stop()
	enqueue := func(taskType string, payload any, topic string) {
		t.Helper()
		bytes, err := sonic.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := producer.Enqueue(&nova.Task{Type: taskType, Payload: bytes}, nova.Queue(topic)); err != nil {
			t.Fatal(err)
		}
	}
	for i, id := range agents {
		command := "printf 'completed' > done"
		if i == 0 {
			command = "printf 'started' > started; sleep 30 & wait; printf 'should-not-run' > done"
		}
		enqueue(taskpayload.TaskTypeJobRun, &taskpayload.JobRunTaskPayload{PipelineID: "pipeline", PipelineRunID: "run", JobRunID: id + "-job", JobName: "wire", AgentID: id, Steps: []taskpayload.StepPayload{{Name: "first", StepRunID: id + "-step", Args: map[string]any{"run": command}}}}, taskpayload.AgentQueue(id))
	}
	waitFor := func(description string, predicate func() bool) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for !predicate() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out: %s", description)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	firstWorkspace := filepath.Join(root, agents[0], "run", agents[0]+"-job")
	secondWorkspace := filepath.Join(root, agents[1], "run", agents[1]+"-job")
	waitFor("first agent starts its job", func() bool { _, err := os.Stat(filepath.Join(firstWorkspace, "started")); return err == nil })
	waitFor("second agent completes its own job", func() bool { _, err := os.Stat(filepath.Join(secondWorkspace, "done")); return err == nil })
	enqueue(taskpayload.TaskTypeJobCancel, taskpayload.JobCancelTaskPayload{AgentID: agents[0], JobRunID: agents[0] + "-job", Reason: "integration test"}, taskpayload.AgentControlQueue(agents[0]))
	waitFor("independent control consumer cancels busy agent", func() bool { _, running := runningJobRunCancel.Load(agents[0] + "-job"); return !running })
	if _, err := os.Stat(filepath.Join(firstWorkspace, "done")); !os.IsNotExist(err) {
		t.Fatalf("cancelled job continued: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, agents[0], "run", agents[1]+"-job")); !os.IsNotExist(err) {
		t.Fatalf("second job ran on the wrong agent: %v", err)
	}
}
