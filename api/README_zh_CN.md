# Arcentra Agent API 中文文档

简体中文 | [English](./README.md)

Arcentra 与 Agent 交互的 gRPC API 定义，使用 Protocol Buffers 定义，通过 Buf 进行管理。

## 概述

本目录包含了 Arcentra 与 Agent 交互的所有的 gRPC API 定义，分为五个主要服务模块：

- **Agent Service** - Agent 端与 Server 端通信的核心接口
- **Pipeline Service** - 流水线管理接口
- **StepRun Service** - 步骤执行（StepRun）管理接口
- **Stream Service** - 实时数据流传输接口
- **Plugin Service** - 插件通信接口

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
└── plugin/v1/                  # Plugin 服务 API
    ├── plugin.proto
    ├── plugin_type.proto
    ├── plugin.pb.go
    ├── plugin_type.pb.go
    └── plugin_grpc.pb.go
```

## API 服务说明

### 1. Agent Service (`agent/v1`)

Agent 端与 Server 端通信的主要接口，负责 Agent 的生命周期管理和步骤执行（StepRun）管理。

**主要功能：**
- **心跳保持** (`Heartbeat`) - Agent 定期向 Server 发送心跳
- **Agent 注册/注销** (`Register`/`Unregister`) - Agent 的生命周期管理
- **步骤执行获取** (`FetchStepRun`) - Agent 主动拉取待执行的步骤执行（StepRun）
- **状态上报** (`ReportStepRunStatus`) - 上报步骤执行状态
- **日志上报** (`ReportStepRunLog`) - 批量上报步骤执行日志
- **步骤执行取消** (`CancelStepRun`) - Server 通知 Agent 取消步骤执行
- **标签更新** (`UpdateLabels`) - 动态更新 Agent 的标签和标记
- **插件管理** (`DownloadPlugin`, `ListAvailablePlugins`) - 插件分发和查询

**核心特性：**
- 支持标签选择器（Label Selector）进行智能步骤执行路由
- 支持插件动态分发
- 完善的指标上报机制

### 2. Pipeline Service (`pipeline/v1`)

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
- **获取流水线运行日志** (`GetPipelineRunLog`) - 获取流水线运行日志

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

### 3. StepRun Service (`steprun/v1`)

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
- **获取日志** (`GetStepRunLog`) - 获取步骤执行日志
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

### 4. Stream Service (`stream/v1`)

实时数据流传输接口，提供双向流式通信能力。

**主要功能：**
- **步骤执行日志流** (`StreamStepRunLog`, `UploadStepRunLog`) - 实时获取和上报步骤执行日志
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

### 5. Plugin Service (`plugin/v1`)

插件通信接口，提供统一的插件执行和管理能力。

**主要功能：**
- **插件信息** (`GetInfo`) - 获取插件元数据（名称、版本、类型、描述）
- **插件指标** (`GetMetrics`) - 获取插件运行时指标（调用次数、错误次数、运行时间）
- **插件初始化** (`Init`) - 使用配置初始化插件
- **插件清理** (`Cleanup`) - 清理插件资源
- **动作执行** (`Execute`) - 所有插件操作的统一入口点
- **配置管理** (`ConfigQuery`, `ConfigQueryByKey`, `ConfigList`) - 查询插件配置

**支持的插件类型：**
- `SOURCE` - 源码管理插件（clone、pull、checkout 等）
- `BUILD` - 构建插件（编译、打包、生成产物等）
- `TEST` - 测试插件（单元测试、集成测试、覆盖率等）
- `DEPLOY` - 部署插件（部署、回滚、扩缩容等）
- `SECURITY` - 安全插件（漏洞扫描、合规检查等）
- `NOTIFY` - 通知插件（邮件、Webhook、即时消息等）
- `APPROVAL` - 审批插件（创建审批、批准、拒绝等）
- `STORAGE` - 存储插件（保存、加载、删除、列表等）
- `ANALYTICS` - 分析插件（事件追踪、查询、指标、报告等）
- `INTEGRATION` - 集成插件（连接、调用、订阅等）
- `CUSTOM` - 自定义插件（特殊用途功能）

**核心特性：**
- 统一的基于动作的执行模型
- 支持动作注册和动态路由
- 主机提供的能力（数据库访问、存储访问）
- 完善的错误处理机制（结构化错误码）
- 运行时指标和监控支持

### Plugin Service 使用示例

#### 执行插件动作

```go
// 执行插件动作
req := &pluginv1.ExecuteRequest{
    Action: "send",  // 动作名称
    Params: []byte(`{"message": "Hello World"}`),  // 动作参数（JSON）
    Opts:   []byte(`{"timeout": 30}`),  // 可选覆盖（JSON）
}

resp, err := client.Execute(context.Background(), req)
if err != nil {
    log.Fatalf("执行插件动作失败: %v", err)
}

if resp.Error != nil {
    log.Fatalf("插件执行错误: %s (代码: %d)", resp.Error.Message, resp.Error.Code)
}

// 解析结果
var result map[string]interface{}
json.Unmarshal(resp.Result, &result)
log.Printf("插件执行结果: %+v", result)
```

#### 获取插件信息

```go
// 获取插件信息
infoResp, err := client.GetInfo(context.Background(), &pluginv1.GetInfoRequest{})
if err != nil {
    log.Fatalf("获取插件信息失败: %v", err)
}

info := infoResp.Info
log.Printf("插件: %s v%s (%s)", info.Name, info.Version, info.Type)
log.Printf("描述: %s", info.Description)
```

#### 查询插件配置

```go
// 查询插件配置
configResp, err := client.ConfigQuery(context.Background(), &pluginv1.ConfigQueryRequest{
    PluginId: "notify",
})
if err != nil {
    log.Fatalf("查询配置失败: %v", err)
}

if configResp.Error != nil {
    log.Fatalf("配置查询错误: %s", configResp.Error.Message)
}

var config map[string]interface{}
json.Unmarshal(configResp.Config, &config)
log.Printf("插件配置: %+v", config)
```

#### 标准动作名称

插件使用统一的基于动作的执行模型。常见的动作名称包括：

**源码插件动作：**
- `clone` - 克隆仓库
- `pull` - 拉取最新更改
- `checkout` - 检出特定分支/提交
- `commit.get` - 获取提交信息
- `commit.diff` - 获取提交差异

**构建插件动作：**
- `build` - 构建项目
- `artifacts.get` - 获取构建产物
- `clean` - 清理构建产物

**通知插件动作：**
- `send` - 发送通知
- `send.template` - 使用模板发送通知
- `send.batch` - 批量发送通知

**存储插件动作：**
- `save` - 保存数据
- `load` - 加载数据
- `delete` - 删除数据
- `list` - 列出项目
- `exists` - 检查项目是否存在

**主机提供的动作：**
- `config.query` - 查询插件配置
- `config.query.key` - 按 key 查询配置
- `config.list` - 列出所有配置

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
- [插件开发指南](../docs/PLUGIN_DEVELOPMENT.md)
- [插件分发指南](../docs/PLUGIN_DISTRIBUTION.md)
- [实现指南](../docs/IMPLEMENTATION_GUIDE.md)
- [Buf 文档](https://docs.buf.build/)
- [gRPC 文档](https://grpc.io/docs/)
- [Protocol Buffers 文档](https://protobuf.dev/)

## 许可证

本项目使用 [LICENSE](../LICENSE) 文件中定义的许可证。
