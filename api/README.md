# Arcentra Agent API

English | [简体中文](./README_zh_CN.md)

gRPC API definitions for the Arcentra and Agent interaction, defined using Protocol Buffers and managed through Buf.

## Overview

This directory contains all gRPC API definitions for the Arcentra and Agent interaction, divided into five main service modules:

- **Agent Service** - Core interface for communication between Agent and Server
- **Pipeline Service** - Pipeline management interface
- **StepRun Service** - StepRun (Step execution) management interface
- **Stream Service** - Real-time data streaming interface
- **Plugin Service** - Plugin communication interface

## Directory Structure

```
api/
├── buf.yaml                    # Buf configuration file (lint and breaking change checks)
├── buf.gen.yaml                # Code generation configuration file
├── README.md                   # English documentation
├── README_zh_CN.md             # Chinese documentation
├── agent/v1/                   # Agent Service API
│   ├── agent.proto             # Proto definition file
│   ├── agent.pb.go             # Generated Go message code
│   └── agent_grpc.pb.go        # Generated gRPC service code
├── pipeline/v1/               # Pipeline Service API
│   ├── pipeline.proto
│   ├── pipeline.pb.go
│   └── pipeline_grpc.pb.go
├── steprun/v1/                 # StepRun Service API
│   ├── steprun.proto
│   ├── steprun.pb.go
│   └── steprun_grpc.pb.go
├── stream/v1/                  # Stream Service API
│   ├── stream.proto
│   ├── stream.pb.go
│   └── stream_grpc.pb.go
└── plugin/v1/                  # Plugin Service API
    ├── plugin.proto
    ├── plugin_type.proto
    ├── plugin.pb.go
    ├── plugin_type.pb.go
    └── plugin_grpc.pb.go
```

## API Service Description

### 1. Agent Service (`agent/v1`)

The main interface for communication between Agent and Server, responsible for Agent lifecycle management and step run execution.

**Main Features:**
- **Heartbeat** (`Heartbeat`) - Agent periodically sends heartbeat to Server
- **Agent Registration/Unregistration** (`Register`/`Unregister`) - Agent lifecycle management
- **StepRun Fetching** (`FetchStepRun`) - Agent actively pulls step runs to execute
- **Status Reporting** (`ReportStepRunStatus`) - Report step run execution status
- **Log Reporting** (`ReportStepRunLog`) - Batch report step run execution logs
- **StepRun Cancellation** (`CancelStepRun`) - Server notifies Agent to cancel step run
- **Label Updates** (`UpdateLabels`) - Dynamically update Agent's labels
- **Plugin Management** (`DownloadPlugin`, `ListAvailablePlugins`) - Plugin distribution and query

**Core Features:**
- Support label selector for intelligent step run routing
- Support dynamic plugin distribution
- Comprehensive metrics reporting mechanism

### 2. Pipeline Service (`pipeline/v1`)

Pipeline management interface, responsible for creating, executing and managing CI/CD pipelines.

**Main Features:**
- **Create Pipeline** (`CreatePipeline`) - Define pipeline configuration
- **Update Pipeline** (`UpdatePipeline`) - Update pipeline configuration
- **Get Pipeline** (`GetPipeline`) - Get pipeline details
- **List Pipelines** (`ListPipelines`) - Paginated pipeline list query
- **Delete Pipeline** (`DeletePipeline`) - Delete pipeline
- **Trigger Execution** (`TriggerPipeline`) - Trigger pipeline execution
- **Stop Pipeline** (`StopPipeline`) - Stop running pipeline
- **Get Pipeline Run** (`GetPipelineRun`) - Get pipeline run details
- **List Pipeline Runs** (`ListPipelineRuns`) - Paginated pipeline run list query
- **Get Pipeline Run Log** (`GetPipelineRunLog`) - Get pipeline run log

**Supported Trigger Methods:**
- Manual trigger (Manual)
- Cron/Schedule trigger (Cron)
- Event trigger (Event/Webhook)

**Pipeline Structure:**
- Supports two modes:
  - `stages` mode: Stage-based pipeline definition (Stage → Jobs → Steps)
  - `jobs` mode: Jobs-only mode (will be automatically wrapped in default Stage)
- Supports complete configuration: Source, Approval, Target, Notify, Triggers

**Pipeline Status:**
- PENDING (Pending)
- RUNNING (Running)
- SUCCESS (Success)
- FAILED (Failed)
- CANCELLED (Cancelled)
- PARTIAL (Partial success)

### 3. StepRun Service (`steprun/v1`)

StepRun (Step execution) management interface, responsible for CRUD operations and execution management of step runs.

According to DSL: Step → StepRun (execution of a Step)

**Main Features:**
- **Create StepRun** (`CreateStepRun`) - Create new step run
- **Get StepRun** (`GetStepRun`) - Get step run details
- **List StepRuns** (`ListStepRuns`) - Paginated step run list query
- **Update StepRun** (`UpdateStepRun`) - Update step run configuration
- **Delete StepRun** (`DeleteStepRun`) - Delete step run
- **Cancel StepRun** (`CancelStepRun`) - Cancel running step run
- **Retry StepRun** (`RetryStepRun`) - Re-execute failed step run
- **Get Log** (`GetStepRunLog`) - Get step run execution log
- **Artifact Management** (`ListStepRunArtifacts`) - Manage step run artifacts

**StepRun Status:**
- PENDING (Pending)
- QUEUED (Queued)
- RUNNING (Running)
- SUCCESS (Success)
- FAILED (Failed)
- CANCELLED (Cancelled)
- TIMEOUT (Timeout)
- SKIPPED (Skipped)

**Core Features:**
- Support plugin-driven execution model (uses + action + args)
- Support failure retry mechanism
- Support artifact collection and management
- Support label selector routing
- Support conditional expressions (when)

### 4. Stream Service (`stream/v1`)

Real-time data streaming interface, providing bidirectional streaming communication capability.

**Main Features:**
- **StepRun Log Stream** (`StreamStepRunLog`, `UploadStepRunLog`) - Real-time get and report step run logs
- **StepRun Status Stream** (`StreamStepRunStatus`) - Real-time push step run status changes
- **Job Status Stream** (`StreamJobStatus`) - Real-time push job (JobRun) status changes
- **Pipeline Status Stream** (`StreamPipelineStatus`) - Real-time push pipeline (PipelineRun) status changes
- **Agent Channel** (`AgentChannel`) - Bidirectional communication between Agent and Server
- **Agent Status Stream** (`StreamAgentStatus`) - Real-time monitor Agent status
- **Event Stream** (`StreamEvents`) - Push system events

**Supported Event Types:**
- StepRun events (created, started, completed, failed, cancelled)
- JobRun events (started, completed, failed, cancelled)
- PipelineRun events (started, completed, failed, cancelled)
- Agent events (registered, unregistered, offline)

### 5. Plugin Service (`plugin/v1`)

Plugin communication interface, providing unified plugin execution and management capabilities.

**Main Features:**
- **Plugin Information** (`GetInfo`) - Get plugin metadata (name, version, type, description)
- **Plugin Metrics** (`GetMetrics`) - Get plugin runtime metrics (call count, error count, uptime)
- **Plugin Initialization** (`Init`) - Initialize plugin with configuration
- **Plugin Cleanup** (`Cleanup`) - Cleanup plugin resources
- **Action Execution** (`Execute`) - Unified entry point for all plugin operations
- **Config Management** (`ConfigQuery`, `ConfigQueryByKey`, `ConfigList`) - Query plugin configurations

**Supported Plugin Types:**
- `SOURCE` - Source code management plugin (clone, pull, checkout, etc.)
- `BUILD` - Build plugin (compile, package, generate artifacts, etc.)
- `TEST` - Test plugin (unit tests, integration tests, coverage, etc.)
- `DEPLOY` - Deployment plugin (deploy, rollback, scaling, etc.)
- `SECURITY` - Security plugin (vulnerability scanning, compliance checks, etc.)
- `NOTIFY` - Notification plugin (email, webhook, instant messaging, etc.)
- `APPROVAL` - Approval plugin (create approval, approve, reject, etc.)
- `STORAGE` - Storage plugin (save, load, delete, list, etc.)
- `ANALYTICS` - Analytics plugin (event tracking, queries, metrics, reports, etc.)
- `INTEGRATION` - Integration plugin (connect, call, subscribe, etc.)
- `CUSTOM` - Custom plugin for special-purpose functionality

**Core Features:**
- Unified action-based execution model
- Support for action registry and dynamic routing
- Host-provided capabilities (database access, storage access)
- Comprehensive error handling with structured error codes
- Runtime metrics and monitoring support

## Quick Start

### Prerequisites

- [Buf CLI](https://docs.buf.build/installation) >= 1.0.0
- [Go](https://golang.org/) >= 1.21
- [Protocol Buffers Compiler](https://grpc.io/docs/protoc-installation/)

### Install Buf

```bash
# macOS
brew install bufbuild/buf/buf

# Linux
curl -sSL "https://github.com/bufbuild/buf/releases/latest/download/buf-$(uname -s)-$(uname -m)" -o /usr/local/bin/buf
chmod +x /usr/local/bin/buf

# Verify installation
buf --version
```

### Generate Code

```bash
# Execute in project root directory
make proto

# Or use buf directly in api directory
cd api
buf generate
```

### Code Check

```bash
# Lint check
buf lint

# Breaking change check
buf breaking --against '.git#branch=main'
```

### Format

```bash
# Format all proto files
buf format -w
```

## Configuration Description

### buf.yaml

Main configuration file, defines:
- Module name: `buf.build/observabil/Arcentra`
- Lint rules: Use STANDARD rule set, but allow streaming RPC
- Breaking change check: Use FILE level check

### buf.gen.yaml

Code generation configuration, defines:
- Go Package prefix: `github.com/arcentrix/arcentra/api`
- Plugin configuration:
  - `protocolbuffers/go` - Generate Go message code
  - `grpc/go` - Generate gRPC service code
- Path mode: `source_relative` (relative to source file generation)

## Usage Examples

### Client Call Example

```go
package main

import (
    "context"
    "log"
    
    "google.golang.org/grpc"
    agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
)

func main() {
    // Connect to gRPC service
    conn, err := grpc.Dial("localhost:50051", grpc.WithInsecure())
    if err != nil {
        log.Fatalf("Connection failed: %v", err)
    }
    defer conn.Close()
    
    // Create client
    client := agentv1.NewAgentServiceClient(conn)
    
    // Call Register RPC
    req := &agentv1.RegisterRequest{
        Ip:                "192.168.1.100",
        Os:                "linux",
        Arch:              "amd64",
        Version:           "1.0.0",
        MaxConcurrentStepRuns: 5,
        Labels: map[string]string{
            "env":  "production",
            "zone": "us-west-1",
        },
    }
    
    resp, err := client.Register(context.Background(), req)
    if err != nil {
        log.Fatalf("Registration failed: %v", err)
    }
    
    log.Printf("Registration successful, Agent ID: %s", resp.AgentId)
}
```

### Server Implementation Example

```go
package main

import (
    "context"
    "log"
    "net"
    
    "google.golang.org/grpc"
    agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
)

type agentService struct {
    agentv1.UnimplementedAgentServiceServer
}

func (s *agentService) Register(ctx context.Context, req *agentv1.RegisterRequest) (*agentv1.RegisterResponse, error) {
    log.Printf("Received registration request: %+v", req)
    
    return &agentv1.RegisterResponse{
        Success:           true,
        Message:           "Registration successful",
        AgentId:           "agent-12345",
        HeartbeatInterval: 30,
    }, nil
}

func main() {
    lis, err := net.Listen("tcp", ":50051")
    if err != nil {
        log.Fatalf("Listen failed: %v", err)
    }
    
    s := grpc.NewServer()
    agentv1.RegisterAgentServiceServer(s, &agentService{})
    
    log.Println("gRPC service started on :50051")
    if err := s.Serve(lis); err != nil {
        log.Fatalf("Service failed to start: %v", err)
    }
}
```

### Streaming RPC Example

```go
// Client: Receive step run logs in real-time
func streamStepRunLog(client streamv1.StreamServiceClient, stepRunID string) {
    req := &streamv1.StreamStepRunLogRequest{
        StepRunId: stepRunID,
        Follow:    true, // Continuously track, similar to tail -f
    }
    
    stream, err := client.StreamStepRunLog(context.Background(), req)
    if err != nil {
        log.Fatalf("Failed to create stream: %v", err)
    }
    
    for {
        resp, err := stream.Recv()
        if err == io.EOF {
            break
        }
        if err != nil {
            log.Fatalf("Receive failed: %v", err)
        }
        
        log.Printf("[%s] %s", resp.LogChunk.Level, resp.LogChunk.Content)
    }
}
```

## Label Selector Usage

Label selectors are used for step run routing, allowing precise control over which Agents execute step runs.

### Simple Matching

```go
// Match Agents with specific labels
labelSelector := &agentv1.AgentSelector{
    MatchLabels: map[string]string{
        "env":  "production",
        "zone": "us-west-1",
        "os":   "linux",
    },
}
```

### Expression Matching

```go
// Use more complex matching rules
labelSelector := &agentv1.AgentSelector{
    MatchExpressions: []*agentv1.LabelExpression{
        {
            Key:      "env",
            Operator: "In",
            Values:   []string{"production", "staging"},
        },
        {
            Key:      "gpu",
            Operator: "Exists",
        },
        {
            Key:      "memory",
            Operator: "Gt",
            Values:   []string{"8192"}, // Memory greater than 8GB
        },
    },
}
```

### Supported Operators

- `In` - Label value is in the specified list
- `NotIn` - Label value is not in the specified list
- `Exists` - Label key exists
- `NotExists` - Label key does not exist
- `Gt` - Label value greater than specified value (for numeric comparison)
- `Lt` - Label value less than specified value (for numeric comparison)

## Plugin Service Usage

### Execute Plugin Action Example

```go
// Execute a plugin action
req := &pluginv1.ExecuteRequest{
    Action: "send",  // Action name
    Params: []byte(`{"message": "Hello World"}`),  // Action parameters (JSON)
    Opts:   []byte(`{"timeout": 30}`),  // Optional overrides (JSON)
}

resp, err := client.Execute(context.Background(), req)
if err != nil {
    log.Fatalf("Failed to execute plugin action: %v", err)
}

if resp.Error != nil {
    log.Fatalf("Plugin execution error: %s (code: %d)", resp.Error.Message, resp.Error.Code)
}

// Parse result
var result map[string]interface{}
json.Unmarshal(resp.Result, &result)
log.Printf("Plugin execution result: %+v", result)
```

### Get Plugin Information Example

```go
// Get plugin information
infoResp, err := client.GetInfo(context.Background(), &pluginv1.GetInfoRequest{})
if err != nil {
    log.Fatalf("Failed to get plugin info: %v", err)
}

info := infoResp.Info
log.Printf("Plugin: %s v%s (%s)", info.Name, info.Version, info.Type)
log.Printf("Description: %s", info.Description)
```

### Query Plugin Configuration Example

```go
// Query plugin configuration
configResp, err := client.ConfigQuery(context.Background(), &pluginv1.ConfigQueryRequest{
    PluginId: "notify",
})
if err != nil {
    log.Fatalf("Failed to query config: %v", err)
}

if configResp.Error != nil {
    log.Fatalf("Config query error: %s", configResp.Error.Message)
}

var config map[string]interface{}
json.Unmarshal(configResp.Config, &config)
log.Printf("Plugin config: %+v", config)
```

### Standard Action Names

Plugins use a unified action-based execution model. Common action names include:

**Source Plugin Actions:**
- `clone` - Clone repository
- `pull` - Pull latest changes
- `checkout` - Checkout specific branch/commit
- `commit.get` - Get commit information
- `commit.diff` - Get commit diff

**Build Plugin Actions:**
- `build` - Build project
- `artifacts.get` - Get build artifacts
- `clean` - Clean build artifacts

**Notify Plugin Actions:**
- `send` - Send notification
- `send.template` - Send notification using template
- `send.batch` - Send batch notifications

**Storage Plugin Actions:**
- `save` - Save data
- `load` - Load data
- `delete` - Delete data
- `list` - List items
- `exists` - Check if item exists

**Host-Provided Actions:**
- `config.query` - Query plugin configuration
- `config.query.key` - Query configuration by key
- `config.list` - List all configurations

## Plugin Distribution Mechanism

Agent Service supports dynamic plugin distribution, supporting three plugin locations:

1. **SERVER** - Server filesystem
2. **STORAGE** - Object storage (S3/OSS/COS/GCS)
3. **REGISTRY** - Plugin registry

### Download Plugin Example

```go
req := &agentv1.DownloadPluginRequest{
    AgentId:  "agent-123",
    PluginId: "notify",
    Version:  "1.0.0", // Optional, download latest if not specified
}

resp, err := client.DownloadPlugin(context.Background(), req)
if err != nil {
    log.Fatalf("Failed to download plugin: %v", err)
}

// Verify checksum
if calculateSHA256(resp.PluginData) != resp.Checksum {
    log.Fatal("Plugin checksum mismatch")
}

// Save plugin
os.WriteFile("plugins/notify.so", resp.PluginData, 0755)
```

## Concept Mapping

According to DSL documentation, runtime model mapping:

| DSL Concept | Runtime Model | Description |
| --- | --- | --- |
| Pipeline | Pipeline | Pipeline definition (static) |
| Stage | Stage | Stage (logical structure, not executed) |
| Job | Job | Job (minimum schedulable and executable unit) |
| Step | Step | Step (sequential operations within a Job) |
| PipelineRun | PipelineRun | Pipeline execution record |
| JobRun | JobRun | Job execution record |
| StepRun | StepRun | Step execution record (managed by StepRun Service) |

## Development Guide

### Modifying Proto Files

1. Modify the corresponding `.proto` file
2. Run `buf lint` to check code style
3. Run `buf breaking --against '.git#branch=main'` to check breaking changes
4. Run `buf generate` to generate new code
5. Commit code

### Adding New RPC Methods

```protobuf
service YourService {
  // Add new method
  rpc NewMethod(NewMethodRequest) returns (NewMethodResponse) {}
}

message NewMethodRequest {
  string param = 1;
}

message NewMethodResponse {
  bool success = 1;
  string message = 2;
}
```

### Version Management

API uses semantic versioning, following these rules:

- **Major version** (`v1`, `v2`) - Incompatible API changes
- **Minor version** - Backward compatible feature additions
- **Patch version** - Backward compatible bug fixes

When introducing breaking changes, create a new version directory (e.g. `agent/v2/`).

## FAQ

### 1. How to handle large file transfers?

For large files (such as plugin binaries), recommend:
- Use streaming RPC for chunked transfer
- Or return pre-signed URL, let client download directly from object storage

### 2. How to handle long-running step runs?

Use Stream Service's streaming interface:
- Real-time push step run status updates
- Real-time push log output
- Use bidirectional stream to maintain connection

### 3. How to implement step run priority?

Add `priority` label in step run's `labels`:
```go
labels: map[string]string{
    "priority": "high",
}
```

Agent can sort by priority when FetchStepRun.

### 4. How to handle Agent disconnect and reconnect?

Agent should:
1. Implement exponential backoff reconnection strategy
2. Re-register after reconnection
3. Report status of incomplete step runs

## Related Documentation

- [Pipeline DSL Documentation](../docs/Pipeline%20DSL.md)
- [Pipeline Schema Documentation](../docs/pipeline_schema.md)
- [Plugin Development Guide](../docs/PLUGIN_DEVELOPMENT.md)
- [Plugin Distribution Guide](../docs/PLUGIN_DISTRIBUTION.md)
- [Implementation Guide](../docs/IMPLEMENTATION_GUIDE.md)
- [Buf Documentation](https://docs.buf.build/)
- [gRPC Documentation](https://grpc.io/docs/)
- [Protocol Buffers Documentation](https://protobuf.dev/)

## Contribution Guide

Contributions welcome! Before submitting PR, please ensure:

1. ✅ All proto files pass `buf lint` check
2. ✅ No breaking changes introduced (or in new version)
3. ✅ Added adequate comments
4. ✅ Generated code is updated
5. ✅ Related documentation is updated

## License

This project uses the license defined in the [LICENSE](../LICENSE) file.
