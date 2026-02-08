# Arcentra Agent API 中文文档

简体中文 | [English](./README.md)

Arcentra 与 Agent 交互的 gRPC API 定义，使用 Protocol Buffers 定义，通过 Buf 进行管理。

## 概述

本目录包含了 Arcentra 与 Agent 交互的所有的 gRPC API 定义，分为五个主要服务模块：

- **Agent Service** - Agent 端与 Server 端通信的核心接口
- **Gateway Service** - 数据面日志与事件接入接口
- **Pipeline Service** - 流水线管理接口
- **StepRun Service** - 步骤执行（StepRun）管理接口
- **Stream Service** - 实时数据流传输接口

## 目录结构

```
api/
├── buf.yaml                    # Buf 配置文件（lint 和 breaking change 检查）
├── buf.gen.yaml                # 代码生成配置文件
├── README.md                   # 英文文档
├── README_zh_CN.md             # 中文文档
├── agent/v1/                   # Agent 服务 API
│   ├── agent.proto             # Proto 定义文件
│   ├── agent.pb.go             # 生成的 Go 消息代码
│   └── agent_grpc.pb.go        # 生成的 gRPC 服务代码
├── gateway/v1/                 # Gateway 服务 API
│   ├── gateway.proto           # Proto 定义文件
│   ├── gateway.pb.go           # 生成的 Go 消息代码
│   └── gateway_grpc.pb.go      # 生成的 gRPC 服务代码
├── pipeline/v1/                # Pipeline 服务 API
│   ├── pipeline.proto
│   ├── pipeline.pb.go
│   └── pipeline_grpc.pb.go
├── steprun/v1/                 # StepRun 服务 API
│   ├── steprun.proto
│   ├── steprun.pb.go
│   └── steprun_grpc.pb.go
├── stream/v1/                  # Stream 服务 API
│   ├── stream.proto
│   ├── stream.pb.go
│   └── stream_grpc.pb.go
```

## API 服务说明

### 1. Agent Service (`agent/v1`)

Agent 端与 Server 端通信的主要接口，负责 Agent 的生命周期管理和步骤执行（StepRun）管理。

**主要功能：**
- **心跳保持** (`Heartbeat`) - Agent 定期向 Server 发送心跳
- **Agent 注册/注销** (`Register`/`Unregister`) - Agent 的生命周期管理
- **步骤执行获取** (`FetchStepRun`) - Agent 主动拉取待执行的步骤执行（StepRun）
- **状态上报** (`ReportStepRunStatus`) - 上报步骤执行状态
- **步骤执行取消** (`CancelStepRun`) - Server 通知 Agent 取消步骤执行
- **标签更新** (`UpdateLabels`) - 动态更新 Agent 的标签和标记
- **控制面连接** (`Connect`) - Agent 与 Gateway 的双向控制通道

**核心特性：**
- 支持标签选择器（Label Selector）进行智能步骤执行路由
- 控制面支持任务下发、取消与状态反馈
- 任务状态更新包含执行指标

### 2. Gateway Service (`gateway/v1`)

数据面接入接口，负责日志与事件的高吞吐写入。

**主要功能：**
- **日志上报** (`PushLogs`) - 高吞吐、批量、允许丢的日志流
- **事件上报** (`PushEvents`) - 可靠 + 幂等的事件流，可重试

**核心特性：**
- 支持批量写入与压缩
- 事件具备幂等 ID，支持部分接受与重试

### 3. Pipeline Service (`pipeline/v1`)

流水线管理接口，负责 CI/CD 流水线的创建、执行和管理。

**主要功能：**
- **创建流水线** (`CreatePipeline`) - 定义流水线配置
- **更新流水线** (`UpdatePipeline`) - 更新流水线配置
- **获取流水线** (`GetPipeline`) - 获取流水线详情
- **列出流水线** (`ListPipelines`) - 分页查询流水线列表
- **删除流水线** (`DeletePipeline`) - 删除流水线
- **触发执行** (`TriggerPipeline`) - 触发流水线执行
- **停止流水线** (`StopPipeline`) - 停止正在运行的流水线
- **获取流水线运行** (`GetPipelineRun`) - 获取流水线运行详情
- **列出流水线运行** (`ListPipelineRuns`) - 分页查询流水线运行列表

**支持的触发方式：**
- 手动触发 (Manual)
- 定时触发 (Cron/Schedule)
- 事件触发 (Event/Webhook)

**Pipeline 结构：**
- 支持两种模式：
  - `stages` 模式：阶段式流水线定义（Stage → Jobs → Steps）
  - `jobs` 模式：仅 Job 模式（将被自动包裹在默认 Stage 中）
- 支持 Source、Approval、Target、Notify、Triggers 等完整配置

**Pipeline 状态：**
- PENDING (等待中)
- RUNNING (运行中)
- SUCCESS (成功)
- FAILED (失败)
- CANCELLED (已取消)
- PARTIAL (部分成功)

### 4. StepRun Service (`steprun/v1`)

步骤执行（StepRun）管理接口，负责 Step 执行的 CRUD 操作和执行管理。

根据 DSL 文档：Step → StepRun（Step 的执行）

**主要功能：**
- **创建步骤执行** (`CreateStepRun`) - 创建新的步骤执行
- **获取步骤执行** (`GetStepRun`) - 获取步骤执行详情
- **列出步骤执行** (`ListStepRuns`) - 分页查询步骤执行列表
- **更新步骤执行** (`UpdateStepRun`) - 更新步骤执行配置
- **删除步骤执行** (`DeleteStepRun`) - 删除步骤执行
- **取消步骤执行** (`CancelStepRun`) - 取消正在执行的步骤执行
- **重试步骤执行** (`RetryStepRun`) - 重新执行失败的步骤执行
- **产物管理** (`ListStepRunArtifacts`) - 管理步骤执行产物

**StepRun 状态：**
- PENDING (等待中)
- QUEUED (已入队)
- RUNNING (运行中)
- SUCCESS (成功)
- FAILED (失败)
- CANCELLED (已取消)
- TIMEOUT (超时)
- SKIPPED (已跳过)

**核心特性：**
- 支持插件驱动的执行模型（uses + action + args）
- 支持失败重试机制
- 支持产物收集和管理
- 支持标签选择器路由
- 支持条件表达式（when）

### 5. Stream Service (`stream/v1`)

实时数据流传输接口，提供双向流式通信能力。

**主要功能：**
- **步骤执行状态流** (`StreamStepRunStatus`) - 实时推送步骤执行状态变化
- **作业状态流** (`StreamJobStatus`) - 实时推送作业（JobRun）状态变化
- **流水线状态流** (`StreamPipelineStatus`) - 实时推送流水线（PipelineRun）状态变化
- **Agent 通道** (`AgentChannel`) - Agent 与 Server 双向通信
- **Agent 状态流** (`StreamAgentStatus`) - 实时监控 Agent 状态
- **事件流** (`StreamEvents`) - 推送系统事件

**支持的事件类型：**
- StepRun 事件（created, started, completed, failed, cancelled）
- JobRun 事件（started, completed, failed, cancelled）
- PipelineRun 事件（started, completed, failed, cancelled）
- Agent 事件（registered, unregistered, offline）

## 快速开始

### 前置要求

- [Buf CLI](https://docs.buf.build/installation) >= 1.0.0
- [Go](https://golang.org/) >= 1.21
- [Protocol Buffers Compiler](https://grpc.io/docs/protoc-installation/)

### 安装 Buf

```bash
# macOS
brew install bufbuild/buf/buf

# Linux
curl -sSL "https://github.com/bufbuild/buf/releases/latest/download/buf-$(uname -s)-$(uname -m)" -o /usr/local/bin/buf
chmod +x /usr/local/bin/buf

# 验证安装
buf --version
```

### 生成代码

```bash
# 在项目根目录下执行
make proto

# 或者在 api 目录下直接使用 buf
cd api
buf generate
```

### 代码检查

```bash
# Lint 检查
buf lint

# Breaking change 检查
buf breaking --against '.git#branch=main'
```

### 格式化

```bash
# 格式化所有 proto 文件
buf format -w
```

## 概念映射

根据 DSL 文档，运行时模型映射如下：

| DSL 概念 | 运行时模型 | 说明 |
| --- | --- | --- |
| Pipeline | Pipeline | 流水线定义（静态） |
| Stage | Stage | 阶段（逻辑结构，不参与执行） |
| Job | Job | 作业（最小可调度、可执行单元） |
| Step | Step | 步骤（Job 内部的顺序操作） |
| PipelineRun | PipelineRun | 流水线执行记录 |
| JobRun | JobRun | 作业执行记录 |
| StepRun | StepRun | 步骤执行记录（StepRun Service 管理） |

## 相关文档

- [Pipeline DSL 文档](../docs/Pipeline%20DSL.md)
- [Pipeline Schema 文档](../docs/pipeline_schema.md)
- [实现指南](../docs/IMPLEMENTATION_GUIDE.md)
- [Buf 文档](https://docs.buf.build/)
- [gRPC 文档](https://grpc.io/docs/)
- [Protocol Buffers 文档](https://protobuf.dev/)

## 许可证

本项目使用 [LICENSE](../LICENSE) 文件中定义的许可证。
