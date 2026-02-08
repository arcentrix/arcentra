# Nova Task Queue

Nova is a flexible and high-performance task queue library for Go that supports multiple message brokers (Kafka, RocketMQ) with unified API.

## Features

- **Multiple Broker Support**: Seamlessly switch between Kafka and RocketMQ
- **Delayed Tasks**: Built-in support for scheduled and delayed task execution
- **Priority Queues**: Support for high, normal, and low priority task queues
- **Batch Processing**: Efficient batch processing with configurable aggregators
- **Message Formats**: Support for JSON, Blob, Protobuf, and Sonic message formats
- **Task Recording**: Optional MySQL-based task status tracking
- **Flexible Configuration**: Option pattern for clean and extensible configuration
- **Thread-Safe**: Fully concurrent-safe implementation

## Supported Brokers

- **Kafka**: Full support with SASL/SSL authentication
- **RocketMQ**: Support with ACL authentication

## Installation

```bash
go get github.com/arcentrix/arcentra/pkg/nova
```

## Quick Start

### Basic Usage with Kafka

```go
package main

import (
    "context"
    "time"
    
    "github.com/arcentrix/arcentra/pkg/taskqueue"
)

func main() {
    // Create a task queue with Kafka
    queue, err := taskqueue.NewTaskQueue(
        taskqueue.WithKafka("localhost:9092"),
        taskqueue.WithGroupID("my-group"),
        taskqueue.WithTopicPrefix("myapp"),
    )
    if err != nil {
        panic(err)
    }
    defer queue.Stop()

    // Enqueue a task
    result, err := queue.Enqueue(&taskqueue.Task{
        Type:    "email",
        Payload: []byte("send email"),
    }, taskqueue.PriorityOpt(taskqueue.PriorityHigh))
    if err != nil {
        panic(err)
    }
    fmt.Printf("Task enqueued: %s\n", result.ID)

    // Start consumer
    err = queue.Start(taskqueue.HandlerFunc(func(ctx context.Context, task *taskqueue.Task) error {
        // Process task
        fmt.Printf("Processing task: %s\n", task.Type)
        return nil
    }))
    if err != nil {
        panic(err)
    }

    // Keep running
    select {}
}
```

## Detailed Usage

### 1. Using Kafka (Basic Configuration)

```go
queue, err := taskqueue.NewTaskQueue(
    taskqueue.WithKafka("localhost:9092"),
    taskqueue.WithGroupID("my-group"),
    taskqueue.WithTopicPrefix("myapp"),
)
if err != nil {
    panic(err)
}
defer queue.Stop()

// Enqueue a delayed task
queue.Enqueue(&taskqueue.Task{
    Type:    "email",
    Payload: []byte("send email"),
}, 
    taskqueue.PriorityOpt(taskqueue.PriorityHigh),
    taskqueue.ProcessIn(5*time.Minute), // Execute after 5 minutes
)

// Start consumer
queue.Start(taskqueue.HandlerFunc(func(ctx context.Context, task *taskqueue.Task) error {
    // Process task
    return nil
}))
```

### 2. Using Kafka (Full Configuration)

```go
queue, err := taskqueue.NewTaskQueue(
    taskqueue.WithKafka(
        "localhost:9092",
        taskqueue.WithKafkaGroupID("my-group"),
        taskqueue.WithKafkaTopicPrefix("myapp"),
        taskqueue.WithKafkaAutoCommit(false),
        taskqueue.WithKafkaSessionTimeout(30000),
        taskqueue.WithKafkaMaxPollInterval(300000),
        // Authentication
        taskqueue.WithKafkaAuth("SASL_SSL", "PLAIN", "username", "password"),
        taskqueue.WithKafkaSSL("/path/to/ca.pem", "/path/to/cert.pem", "/path/to/key.pem", ""),
    ),
    taskqueue.WithDelaySlots(24, time.Hour), // 24 delay slots, 1 hour each
)
if err != nil {
    panic(err)
}
defer queue.Stop()
```

### 3. Using RocketMQ

```go
import "github.com/apache/rocketmq-client-go/v2/consumer"

queue, err := taskqueue.NewTaskQueue(
    taskqueue.WithRocketMQ(
        []string{"127.0.0.1:9876"},
        taskqueue.WithRocketMQGroupID("my-group"),
        taskqueue.WithRocketMQTopicPrefix("myapp"),
        taskqueue.WithRocketMQConsumerModel(consumer.Clustering),
        taskqueue.WithRocketMQConsumeTimeout(5*time.Minute),
        taskqueue.WithRocketMQMaxReconsumeTimes(3),
        // Authentication
        taskqueue.WithRocketMQAuth("accessKey", "secretKey"),
    ),
    taskqueue.WithGroupID("my-group"),
    taskqueue.WithTopicPrefix("myapp"),
)
if err != nil {
    panic(err)
}
defer queue.Stop()
```

### 4. Batch Processing with Aggregators

The library provides three types of aggregators for batch processing:

#### Count Aggregator

Flushes when the task count reaches the threshold:

```go
queue, err := taskqueue.NewTaskQueue(
    taskqueue.WithKafka("localhost:9092"),
    taskqueue.WithGroupID("my-group"),
    taskqueue.WithTopicPrefix("myapp"),
)
if err != nil {
    panic(err)
}
defer queue.Stop()

// Create count aggregator (flushes when 100 tasks accumulated)
aggregator := taskqueue.NewCountAggregator(100)

// Start batch consumer
queue.StartBatch(
    taskqueue.IBatchHandlerFunc(func(ctx context.Context, tasks []*taskqueue.Task) error {
        // Process tasks in batch
        fmt.Printf("Processing %d tasks\n", len(tasks))
        return nil
    }),
    aggregator,
)

// Enqueue batch
tasks := []*taskqueue.Task{
    {Type: "email", Payload: []byte("email1")},
    {Type: "email", Payload: []byte("email2")},
}
queue.EnqueueBatch(tasks, taskqueue.PriorityOpt(taskqueue.PriorityNormal))
```

#### Time Aggregator

Flushes when the time window is reached:

```go
// Create time aggregator (flushes every 10 seconds)
aggregator := taskqueue.NewTimeAggregator(10 * time.Second)

queue.StartBatch(
    taskqueue.IBatchHandlerFunc(func(ctx context.Context, tasks []*taskqueue.Task) error {
        // Process tasks in batch
        return nil
    }),
    aggregator,
)
```

#### Time-Count Aggregator

Flushes when either the time window is reached or the task count threshold is reached:

```go
// Create time-count aggregator (flushes when 100 tasks OR 10 seconds elapsed)
aggregator := taskqueue.NewTimeCountAggregator(100, 10*time.Second)

queue.StartBatch(
    taskqueue.IBatchHandlerFunc(func(ctx context.Context, tasks []*taskqueue.Task) error {
        // Process tasks in batch
        return nil
    }),
    aggregator,
)
```

### 6. Message Formats

The library supports multiple message formats:

```go
// JSON (default)
queue, err := taskqueue.NewTaskQueue(
    taskqueue.WithKafka("localhost:9092"),
    taskqueue.WithMessageFormat(taskqueue.MessageFormatJSON),
)

// Blob (raw bytes)
queue, err := taskqueue.NewTaskQueue(
    taskqueue.WithKafka("localhost:9092"),
    taskqueue.WithMessageFormat(taskqueue.MessageFormatBlob),
)

// Protobuf
queue, err := taskqueue.NewTaskQueue(
    taskqueue.WithKafka("localhost:9092"),
    taskqueue.WithMessageFormat(taskqueue.MessageFormatProtobuf),
)

// Sonic (high-performance JSON)
queue, err := taskqueue.NewTaskQueue(
    taskqueue.WithKafka("localhost:9092"),
    taskqueue.WithMessageFormat(taskqueue.MessageFormatSonic),
)
```

### 7. Task Recording (MySQL)

Optional task status tracking with MySQL:

```go
import (
    "gorm.io/driver/mysql"
    "gorm.io/gorm"
)

dsn := "user:password@tcp(localhost:3306)/database?charset=utf8mb4&parseTime=True&loc=Local"
db, _ := gorm.Open(mysql.Open(dsn), &gorm.Config{})
recorder, _ := taskqueue.NewMySQLTaskRecorder(db, "")

queue, err := taskqueue.NewTaskQueue(
    taskqueue.WithKafka("localhost:9092"),
    taskqueue.WithTaskRecorder(recorder),
)
```

## Task Options

When enqueueing tasks, you can specify various options:

```go
// Priority
queue.Enqueue(task, taskqueue.PriorityOpt(taskqueue.PriorityHigh))
queue.Enqueue(task, taskqueue.PriorityOpt(taskqueue.PriorityNormal))
queue.Enqueue(task, taskqueue.PriorityOpt(taskqueue.PriorityLow))

// Scheduled execution time
queue.Enqueue(task, taskqueue.ProcessAt(time.Now().Add(1*time.Hour)))
queue.Enqueue(task, taskqueue.ProcessIn(30*time.Minute))

// Custom queue name
queue.Enqueue(task, taskqueue.Queue("custom-queue"))

// Combine options
queue.Enqueue(task,
    taskqueue.PriorityOpt(taskqueue.PriorityHigh),
    taskqueue.ProcessIn(5*time.Minute),
    taskqueue.Queue("important-tasks"),
)
```

## Configuration Options

### Common Options

- `WithGroupID(groupID string)`: Set consumer group ID
- `WithTopicPrefix(prefix string)`: Set topic prefix
- `WithDelaySlots(count int, duration time.Duration)`: Configure delay slots
- `WithMessageFormat(format MessageFormat)`: Set message format
- `WithMessageCodec(codec MessageCodec)`: Set custom message codec
- `WithTaskRecorder(recorder TaskRecorder)`: Set task recorder

### Kafka Options

- `WithKafkaGroupID(groupID string)`: Set Kafka consumer group ID
- `WithKafkaTopicPrefix(prefix string)`: Set Kafka topic prefix
- `WithKafkaAutoCommit(autoCommit bool)`: Enable/disable auto commit
- `WithKafkaSessionTimeout(timeout int)`: Set session timeout (ms)
- `WithKafkaMaxPollInterval(interval int)`: Set max poll interval (ms)
- `WithKafkaDelaySlots(count int, duration time.Duration)`: Configure delay slots
- `WithKafkaAuth(protocol, mechanism, username, password string)`: Set authentication
- `WithKafkaSSL(caFile, certFile, keyFile, password string)`: Set SSL configuration

### RocketMQ Options

- `WithRocketMQGroupID(groupID string)`: Set RocketMQ consumer group ID
- `WithRocketMQTopicPrefix(prefix string)`: Set RocketMQ topic prefix
- `WithRocketMQConsumerModel(model MessageModel)`: Set consumer model
- `WithRocketMQConsumeTimeout(timeout time.Duration)`: Set consume timeout
- `WithRocketMQMaxReconsumeTimes(times int32)`: Set max retry times
- `WithRocketMQAuth(accessKey, secretKey string)`: Set ACL authentication
- `WithRocketMQCredentials(credentials *primitive.Credentials)`: Set credentials
- `WithRocketMQDelaySlots(count int, duration time.Duration)`: Configure delay slots

## API Reference

### TaskQueue Interface

```go
type TaskQueue interface {
    // Enqueue a single task
    Enqueue(task *Task, opts ...Option) (*Result, error)
    
    // Enqueue multiple tasks
    EnqueueBatch(tasks []*Task, opts ...Option) (*Result, error)
    
    // Start consumer with single task handler
    Start(handler IHandler) error
    
    // Start consumer with batch handler and aggregator
    StartBatch(handler IBatchHandler, agg IAggregator) error
    
    // Stop the consumer
    Stop() error
}
```

### Task Structure

```go
type Task struct {
    Type    string // Task type identifier
    Payload []byte // Task data
}
```

### Priority Levels

- `PriorityHigh`: High priority (value: 3)
- `PriorityNormal`: Normal priority (value: 2)
- `PriorityLow`: Low priority (value: 1)

## Best Practices

1. **Always call `Stop()`**: Make sure to call `queue.Stop()` when shutting down to properly clean up resources.

2. **Error Handling**: Always check errors returned from `NewTaskQueue`, `Start`, and `Enqueue` methods.

3. **Context Usage**: Use context for cancellation and timeout in your handlers.

4. **Batch Processing**: Use batch processing for high-throughput scenarios to reduce overhead.

5. **Message Format**: Choose the appropriate message format based on your needs:
   - JSON: Human-readable, good for debugging
   - Protobuf: Compact, efficient for production
   - Sonic: Fast JSON parsing for high-performance scenarios
   - Blob: For raw binary data

6. **Delay Slots**: Configure delay slots based on your delay requirements. More slots provide finer granularity but consume more resources.

## License

[Your License Here]
