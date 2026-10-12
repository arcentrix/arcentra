package executor

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/pkg/logstream"
	kafkaconfig "github.com/arcentrix/arcentra/pkg/mq/kafka"
	"github.com/bytedance/sonic"
	ckafka "github.com/confluentinc/confluent-kafka-go/v2/kafka"
)

func TestReliableBuildLogsSurviveKafkaBrokerOutage(t *testing.T) {
	if testing.Short() {
		t.Skip("Kafka wire-protocol mock cluster")
	}
	cluster, err := ckafka.NewMockCluster(1)
	if err != nil {
		t.Fatal(err)
	}
	defer cluster.Close()
	if err = cluster.CreateTopic(buildLogsTopic, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err = cluster.SetBrokerDown(1); err != nil {
		t.Fatal(err)
	}
	pub, err := NewReliableKafkaLogPublisher(kafkaconfig.Config{BootstrapServers: cluster.BootstrapServers(), SecurityProtocol: "PLAINTEXT", Acks: "0"}, "durable-test", logstream.SpoolConfig{Directory: filepath.Join(t.TempDir(), "spool"), MaxDiskUsageMB: 1}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	event := BuildLogMessageFromEvent(EventContext{PipelineID: "pipeline", PipelineRunID: "run", JobRunID: "job", StepID: "step"}, "retained during broker outage", "stdout")
	if err = pub.Publish(ctx, event); err != nil {
		t.Fatalf("local durable acceptance failed: %v", err)
	}
	if err = cluster.SetBrokerUp(1); err != nil {
		t.Fatal(err)
	}
	consumer, err := ckafka.NewConsumer(&ckafka.ConfigMap{"bootstrap.servers": cluster.BootstrapServers(), "group.id": "durable-test", "auto.offset.reset": "earliest", "enable.auto.commit": false})
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()
	if err = consumer.Subscribe(buildLogsTopic, nil); err != nil {
		t.Fatal(err)
	}
	message, err := consumer.ReadMessage(15 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var got logstream.BuildLogMessage
	if err = sonic.Unmarshal(message.Value, &got); err != nil {
		t.Fatal(err)
	}
	if got.EventID != event.EventID || got.Content != event.Content || got.PipelineRunID != "run" || got.JobID != "job" {
		t.Fatalf("wrong replayed message: %+v", got)
	}
}
