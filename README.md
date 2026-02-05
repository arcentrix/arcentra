# Arcentra CI/CD 平台 API 文档

## 架构说明

- **Server端**：负责任务调度、流水线管理、状态监控
- **Agent端**：负责执行具体的任务，上报状态和日志
- **通信方式**：gRPC（支持单向RPC和双向流）

## API 服务

### 1. Agent服务 (`api/agent/v1/agent.proto`)

Agent端与Server端通信的主要接口，负责Agent生命周期管理和任务执行。

#### 核心功能

- **Agent生命周期管理**
  - `Register`: Agent注册，携带主机信息、标签、容量等
  - `Unregister`: Agent注销
  - `Heartbeat`: 定期心跳保持连接
  - `UpdateLabels`: 动态更新Agent的labels和tags

- **任务执行**
  - `FetchJob`: Agent主动拉取待执行任务（基于label匹配）
  - `ReportJobStatus`: 上报任务执行状态变化
  - `ReportJobLog`: 批量上报任务日志
  - `CancelJob`: 接收取消任务的指令

#### 关键数据模型

- `AgentStatus`: Agent状态（在线/离线/忙碌/空闲）
- `JobStatus`: 任务状态（等待/运行中/成功/失败/取消/超时）
- `Job`: 任务定义（命令、环境变量、超时、镜像、产物等）

### 2. Job服务 (`api/job/v1/job.proto`)

任务和流水线管理接口，提供完整的CRUD操作。

#### 核心功能

- **任务管理**
  - `CreateJob`: 创建任务
  - `GetJob`: 获取任务详情
  - `ListJobs`: 列出任务（支持分页和过滤）
  - `UpdateJob`: 更新任务配置
  - `DeleteJob`: 删除任务
  - `CancelJob`: 取消正在执行的任务
  - `RetryJob`: 重试失败的任务
  - `GetJobLog`: 获取任务日志
  - `ListJobArtifacts`: 列出任务产物

- **流水线管理**
  - `CreatePipeline`: 创建流水线
  - `GetPipeline`: 获取流水线详情
  - `ListPipelines`: 列出流水线
  - `TriggerPipeline`: 触发流水线执行
  - `StopPipeline`: 停止流水线

#### 关键数据模型

- `JobDetail`: 任务详细信息（状态、命令、时间、Agent、退出码等）
- `PipelineDetail`: 流水线详细信息（阶段、环境变量、触发方式、统计数据等）
- `Stage`: 流水线阶段配置
- `ArtifactConfig`: 产物配置（路径、过期时间等）

### 3. Stream服务 (`api/job/v1/stream.proto`)

实时数据流传输接口，提供日志流、状态流、事件流等。

#### 核心功能

- **日志流**
  - `StreamJobLog`: 实时获取任务日志流（类似tail -f）
  - `UploadJobLog`: Agent流式上报日志

- **状态流**
  - `StreamJobStatus`: 实时监控任务状态变化
  - `StreamPipelineStatus`: 实时监控流水线状态
  - `StreamAgentStatus`: 实时监控Agent状态

- **双向通信**
  - `AgentChannel`: Agent与Server双向流通信
    - Agent发送：心跳、状态更新、日志、任务请求、指标
    - Server响应：心跳确认、任务分配、取消命令、配置更新

- **事件流**
  - `StreamEvents`: 实时推送系统事件（任务创建/完成、流水线状态、Agent上下线等）

#### 关键数据模型

- `LogChunk`: 日志块（时间戳、行号、级别、内容、流类型）
- `AgentMetrics`: Agent指标（CPU、内存、磁盘使用率等）
- `EventType`: 事件类型枚举

## 通信流程示例

### Agent启动流程

```
1. Agent -> Server: Register (注册)
2. Server -> Agent: RegisterResponse (返回Agent ID和配置)
3. Agent -> Server: Heartbeat (定期心跳)
4. Agent -> Server: FetchJob (拉取任务)
5. Server -> Agent: FetchJobResponse (返回待执行任务)
```

### 任务执行流程

```
1. Agent收到任务
2. Agent -> Server: ReportJobStatus (RUNNING)
3. Agent -> Server: ReportJobLog (流式上报日志)
4. Agent执行任务
5. Agent -> Server: ReportJobStatus (SUCCESS/FAILED)
```

### 流水线触发流程

```
1. Client -> Server: TriggerPipeline
2. Server创建流水线任务
3. Agent -> Server: FetchJob (拉取任务)
4. Client -> Server: StreamPipelineStatus (监控流水线状态)
5. Server -> Client: 流式推送状态变化
```

## Label系统

### Label概述

Label是Agent和任务匹配的核心机制，通过灵活的标签选择器（LabelSelector）实现任务到Agent的智能路由。

### Label使用场景

1. **环境隔离**：`env=production`, `env=staging`, `env=dev`
2. **地域分布**：`region=us-west`, `region=cn-north`
3. **硬件能力**：`gpu=true`, `cpu=high-performance`
4. **专用任务**：`build=android`, `deploy=kubernetes`
5. **版本控制**：`agent-version=v1.2.0`

### LabelSelector支持

#### 1. 精确匹配（match_labels）

```protobuf
// 匹配 env=production AND region=us-west 的Agent
label_selector {
  match_labels {
    "env": "production",
    "region": "us-west"
  }
}
```

#### 2. 表达式匹配（match_expressions）

支持6种操作符：
- **IN**: 标签值在列表中
- **NOT_IN**: 标签值不在列表中
- **EXISTS**: 标签key存在
- **NOT_EXISTS**: 标签key不存在
- **GT**: 标签值大于指定值（数值比较）
- **LT**: 标签值小于指定值（数值比较）

```protobuf
// 匹配 env in [staging, production] 的Agent
label_selector {
  match_expressions {
    key: "env"
    operator: LABEL_OPERATOR_IN
    values: ["staging", "production"]
  }
}

// 匹配有GPU且CPU核心数大于8的Agent
label_selector {
  match_expressions {
    key: "gpu"
    operator: LABEL_OPERATOR_EXISTS
  }
  match_expressions {
    key: "cpu-cores"
    operator: LABEL_OPERATOR_GT
    values: ["8"]
  }
}
```

### Agent Label管理

1. **注册时设置**：通过`Register`接口的`labels`字段
2. **心跳更新**：通过`Heartbeat`接口的`labels`字段动态更新
3. **主动更新**：通过`UpdateLabels`接口更新（支持merge和replace模式）

### Label最佳实践

1. **使用有意义的key**：如`environment`、`region`、`capability`
2. **值使用小写**：保持一致性，避免大小写问题
3. **避免敏感信息**：不要在label中存储密码、密钥等
4. **合理使用层次**：如`team/project`、`owner/team`
5. **保持简洁**：每个Agent建议不超过20个labels

## 特性亮点

1. **智能标签路由**：通过强大的LabelSelector实现任务到Agent的精确匹配
2. **流式日志**：支持实时日志推送和历史日志查询
3. **双向通信**：Agent与Server建立长连接，减少轮询开销
4. **任务编排**：支持阶段（Stage）和依赖（depends_on）
5. **产物管理**：支持产物收集、过期策略
6. **容器支持**：支持Docker镜像执行
7. **重试机制**：支持任务失败自动重试
8. **实时监控**：全面的状态流和事件流
9. **分页查询**：所有列表接口支持分页和排序
10. **动态标签**：Agent可动态更新labels，无需重启
11. **插件系统**：支持插件热加载、自动监控、动态扩展功能

## 插件系统

Arcentra 提供了强大的插件系统，支持动态扩展功能。插件系统具有以下特性：

### 插件类型

支持6种插件类型：

- **CI 插件**：构建、测试、代码检查
- **CD 插件**：部署、回滚
- **Security 插件**：安全扫描、审计
- **Notify 插件**：消息通知（Slack、邮件、钉钉等）
- **Storage 插件**：存储管理
- **Custom 插件**：自定义功能扩展

### 自动加载功能

插件系统支持**自动监控**和**热加载**，无需重启服务：

#### 1. 自动加载新插件

```bash
# 将插件文件放入监控目录
cp my-plugin.so plugins/

# 系统自动检测并加载，无需重启
```

#### 2. 自动卸载插件

```bash
# 删除插件文件
rm plugins/my-plugin.so

# 系统自动卸载该插件
```

#### 3. 配置文件热重载

```bash
# 修改配置文件
vim conf.d/plugins.yaml

# 保存后自动生效
```

### 使用示例

```go
import "github.com/arcentrix/arcentra/pkg/plugin"

// 创建插件管理器
manager := plugin.NewManager()

// 从配置加载插件
manager.LoadPluginsFromConfig("./conf.d/plugins.yaml")
manager.Init(context.Background())

// 启动自动监控
watchDirs := []string{"./plugins"}
manager.StartAutoWatch(watchDirs, "./conf.d/plugins.yaml")
defer manager.StopAutoWatch()

// 使用插件
notifyPlugin, err := manager.GetNotifyPlugin("slack")
if err == nil {
    notifyPlugin.Send(ctx, message)
}
```

### 运行演示程序

```bash
# 运行插件自动加载演示
go run examples/plugin_autowatch/main.go

# 演示会监控 ./plugins 目录
# 你可以在运行时添加/删除插件，观察自动加载效果
```

### 更多文档

- [插件快速开始](./docs/PLUGIN_QUICKSTART.md) - 5分钟快速体验
- [插件开发指南](./docs/PLUGIN_DEVELOPMENT.md) - 完整开发教程
- [插件快速参考](./docs/PLUGIN_REFERENCE.md) - 代码片段和命令速查
- [插件自动加载](./docs/PLUGIN_AUTO_LOAD.md) - 自动监控详细说明

## 代码生成

### 使用Makefile（推荐）

```bash
# 1. 首次使用，安装protoc插件
make proto-install

# 2. 生成proto代码
make proto

# 3. 清理生成的代码（如需要）
make proto-clean
```

### 手动生成

```bash
# 生成Go代码
protoc --go_out=. --go_opt=paths=source_relative \
       --go-grpc_out=. --go-grpc_opt=paths=source_relative \
       api/agent/v1/*.proto

protoc --go_out=. --go_opt=paths=source_relative \
       --go-grpc_out=. --go-grpc_opt=paths=source_relative \
       api/job/v1/*.proto
```

### 前置要求

1. **安装protoc编译器**：
   ```bash
   # macOS
   brew install protobuf
   
   # Ubuntu/Debian
   apt-get install -y protobuf-compiler
   
   # CentOS/RHEL
   yum install -y protobuf-compiler
   ```

2. **安装Go插件**（运行`make proto-install`会自动安装）：
   ```bash
   go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
   go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
   ```

