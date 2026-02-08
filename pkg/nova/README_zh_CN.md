# Nova 任务队列

Nova 是一个灵活且高性能的 Go 任务队列库，支持多种消息代理（Kafka、RocketMQ），提供统一的 API。

## 特性

- **多代理支持**：可在 Kafka、RocketMQ 之间无缝切换
- **延迟任务**：内置支持定时和延迟任务执行
- **优先级队列**：支持高、中、低优先级任务队列
- **批量处理**：高效的批量处理，支持可配置的聚合器
- **消息格式**：支持 JSON、Blob、Protobuf 和 Sonic 消息格式
- **任务记录**：可选的基于 MySQL 的任务状态跟踪
- **灵活配置**：使用选项模式实现清晰且可扩展的配置
- **线程安全**：完全并发安全的实现

## 支持的代理

- **Kafka**：完整支持，包含 SASL/SSL 认证
- **RocketMQ**：支持 ACL 认证

## 安装

```bash
go get github.com/arcentrix/arcentra/pkg/nova
```

## 快速开始

### Kafka 基础使用

```go
package main

import (
    "context"
    "fmt"
    "time"
    
    "github.com/arcentrix/arcentra/pkg/taskqueue"
)

func main() {
    // 使用 Kafka 创建任务队列
    queue, err := taskqueue.NewTaskQueue(
        taskqueue.WithKafka("localhost:9092"),
        taskqueue.WithGroupID("my-group"),
        taskqueue.WithTopicPrefix("myapp"),
    )
    if err != nil {
        panic(err)
    }
    defer queue.Stop()

    // 入队一个任务
    result, err := queue.Enqueue(&taskqueue.Task{
        Type:    "email",
        Payload: []byte("send email"),
    }, taskqueue.PriorityOpt(taskqueue.PriorityHigh))
    if err != nil {
        panic(err)
    }
    fmt.Printf("任务已入队: %s\n", result.ID)

    // 启动消费者
    err = queue.Start(taskqueue.HandlerFunc(func(ctx context.Context, task *taskqueue.Task) error {
        // 处理任务
        fmt.Printf("处理任务: %s\n", task.Type)
        return nil
    }))
    if err != nil {
        panic(err)
    }

    // 保持运行
    select {}
}
```

## 详细使用

### 1. 使用 Kafka（基础配置）

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

// 入队一个延迟任务
queue.Enqueue(&taskqueue.Task{
    Type:    "email",
    Payload: []byte("send email"),
}, 
    taskqueue.PriorityOpt(taskqueue.PriorityHigh),
    taskqueue.ProcessIn(5*time.Minute), // 5 分钟后执行
)

// 启动消费者
queue.Start(taskqueue.HandlerFunc(func(ctx context.Context, task *taskqueue.Task) error {
    // 处理任务
    return nil
}))
```

### 2. 使用 Kafka（完整配置）

```go
queue, err := taskqueue.NewTaskQueue(
    taskqueue.WithKafka(
        "localhost:9092",
        taskqueue.WithKafkaGroupID("my-group"),
        taskqueue.WithKafkaTopicPrefix("myapp"),
        taskqueue.WithKafkaAutoCommit(false),
        taskqueue.WithKafkaSessionTimeout(30000),
        taskqueue.WithKafkaMaxPollInterval(300000),
        // 认证
        taskqueue.WithKafkaAuth("SASL_SSL", "PLAIN", "username", "password"),
        taskqueue.WithKafkaSSL("/path/to/ca.pem", "/path/to/cert.pem", "/path/to/key.pem", ""),
    ),
    taskqueue.WithDelaySlots(24, time.Hour), // 24 个延迟槽，每个 1 小时
)
if err != nil {
    panic(err)
}
defer queue.Stop()
```

### 3. 使用 RocketMQ

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
        // 认证
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

### 4. 批量处理与聚合器

库提供了三种类型的聚合器用于批量处理：

#### 数量聚合器

当任务数量达到阈值时刷新：

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

// 创建数量聚合器（当累积 100 个任务时刷新）
aggregator := taskqueue.NewCountAggregator(100)

// 启动批量消费者
queue.StartBatch(
    taskqueue.IBatchHandlerFunc(func(ctx context.Context, tasks []*taskqueue.Task) error {
        // 批量处理任务
        fmt.Printf("处理 %d 个任务\n", len(tasks))
        return nil
    }),
    aggregator,
)

// 批量入队
tasks := []*taskqueue.Task{
    {Type: "email", Payload: []byte("email1")},
    {Type: "email", Payload: []byte("email2")},
}
queue.EnqueueBatch(tasks, taskqueue.PriorityOpt(taskqueue.PriorityNormal))
```

#### 时间聚合器

当时间窗口到达时刷新：

```go
// 创建时间聚合器（每 10 秒刷新一次）
aggregator := taskqueue.NewTimeAggregator(10 * time.Second)

queue.StartBatch(
    taskqueue.IBatchHandlerFunc(func(ctx context.Context, tasks []*taskqueue.Task) error {
        // 批量处理任务
        return nil
    }),
    aggregator,
)
```

#### 时间-数量聚合器

当时间窗口到达或任务数量达到阈值时刷新：

```go
// 创建时间-数量聚合器（当 100 个任务或 10 秒过去时刷新）
aggregator := taskqueue.NewTimeCountAggregator(100, 10*time.Second)

queue.StartBatch(
    taskqueue.IBatchHandlerFunc(func(ctx context.Context, tasks []*taskqueue.Task) error {
        // 批量处理任务
        return nil
    }),
    aggregator,
)
```

### 6. 消息格式

库支持多种消息格式：

```go
// JSON（默认）
queue, err := taskqueue.NewTaskQueue(
    taskqueue.WithKafka("localhost:9092"),
    taskqueue.WithMessageFormat(taskqueue.MessageFormatJSON),
)

// Blob（原始字节）
queue, err := taskqueue.NewTaskQueue(
    taskqueue.WithKafka("localhost:9092"),
    taskqueue.WithMessageFormat(taskqueue.MessageFormatBlob),
)

// Protobuf
queue, err := taskqueue.NewTaskQueue(
    taskqueue.WithKafka("localhost:9092"),
    taskqueue.WithMessageFormat(taskqueue.MessageFormatProtobuf),
)

// Sonic（高性能 JSON）
queue, err := taskqueue.NewTaskQueue(
    taskqueue.WithKafka("localhost:9092"),
    taskqueue.WithMessageFormat(taskqueue.MessageFormatSonic),
)
```

### 7. 任务记录（MySQL）

可选的基于 MySQL 的任务状态跟踪：

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

## 任务选项

在入队任务时，可以指定各种选项：

```go
// 优先级
queue.Enqueue(task, taskqueue.PriorityOpt(taskqueue.PriorityHigh))
queue.Enqueue(task, taskqueue.PriorityOpt(taskqueue.PriorityNormal))
queue.Enqueue(task, taskqueue.PriorityOpt(taskqueue.PriorityLow))

// 计划执行时间
queue.Enqueue(task, taskqueue.ProcessAt(time.Now().Add(1*time.Hour)))
queue.Enqueue(task, taskqueue.ProcessIn(30*time.Minute))

// 自定义队列名称
queue.Enqueue(task, taskqueue.Queue("custom-queue"))

// 组合选项
queue.Enqueue(task,
    taskqueue.PriorityOpt(taskqueue.PriorityHigh),
    taskqueue.ProcessIn(5*time.Minute),
    taskqueue.Queue("important-tasks"),
)
```

## 配置选项

### 通用选项

- `WithGroupID(groupID string)`: 设置消费者组 ID
- `WithTopicPrefix(prefix string)`: 设置主题前缀
- `WithDelaySlots(count int, duration time.Duration)`: 配置延迟槽
- `WithMessageFormat(format MessageFormat)`: 设置消息格式
- `WithMessageCodec(codec MessageCodec)`: 设置自定义消息编解码器
- `WithTaskRecorder(recorder TaskRecorder)`: 设置任务记录器

### Kafka 选项

- `WithKafkaGroupID(groupID string)`: 设置 Kafka 消费者组 ID
- `WithKafkaTopicPrefix(prefix string)`: 设置 Kafka 主题前缀
- `WithKafkaAutoCommit(autoCommit bool)`: 启用/禁用自动提交
- `WithKafkaSessionTimeout(timeout int)`: 设置会话超时（毫秒）
- `WithKafkaMaxPollInterval(interval int)`: 设置最大轮询间隔（毫秒）
- `WithKafkaDelaySlots(count int, duration time.Duration)`: 配置延迟槽
- `WithKafkaAuth(protocol, mechanism, username, password string)`: 设置认证
- `WithKafkaSSL(caFile, certFile, keyFile, password string)`: 设置 SSL 配置

### RocketMQ 选项

- `WithRocketMQGroupID(groupID string)`: 设置 RocketMQ 消费者组 ID
- `WithRocketMQTopicPrefix(prefix string)`: 设置 RocketMQ 主题前缀
- `WithRocketMQConsumerModel(model MessageModel)`: 设置消费者模式
- `WithRocketMQConsumeTimeout(timeout time.Duration)`: 设置消费超时
- `WithRocketMQMaxReconsumeTimes(times int32)`: 设置最大重试次数
- `WithRocketMQAuth(accessKey, secretKey string)`: 设置 ACL 认证
- `WithRocketMQCredentials(credentials *primitive.Credentials)`: 设置凭证
- `WithRocketMQDelaySlots(count int, duration time.Duration)`: 配置延迟槽

## API 参考

### TaskQueue 接口

```go
type TaskQueue interface {
    // 入队单个任务
    Enqueue(task *Task, opts ...Option) (*Result, error)
    
    // 入队多个任务
    EnqueueBatch(tasks []*Task, opts ...Option) (*Result, error)
    
    // 使用单任务处理器启动消费者
    Start(handler IHandler) error
    
    // 使用批量处理器和聚合器启动消费者
    StartBatch(handler IBatchHandler, agg IAggregator) error
    
    // 停止消费者
    Stop() error
}
```

### Task 结构

```go
type Task struct {
    Type    string // 任务类型标识符
    Payload []byte // 任务数据
}
```

### 优先级级别

- `PriorityHigh`: 高优先级（值：3）
- `PriorityNormal`: 普通优先级（值：2）
- `PriorityLow`: 低优先级（值：1）

## 最佳实践

1. **始终调用 `Stop()`**：确保在关闭时调用 `queue.Stop()` 以正确清理资源。

2. **错误处理**：始终检查 `NewTaskQueue`、`Start` 和 `Enqueue` 方法返回的错误。

3. **上下文使用**：在处理函数中使用 context 进行取消和超时控制。

4. **批量处理**：在高吞吐量场景中使用批量处理以减少开销。

5. **消息格式**：根据需求选择适当的消息格式：
   - JSON：人类可读，适合调试
   - Protobuf：紧凑，适合生产环境
   - Sonic：快速 JSON 解析，适合高性能场景
   - Blob：用于原始二进制数据

6. **延迟槽**：根据延迟需求配置延迟槽。更多槽提供更细的粒度，但会消耗更多资源。

## 许可证

[您的许可证]
