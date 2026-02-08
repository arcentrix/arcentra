# Arcentra Agent API

English | [简体中文](https://github.com/arcentrix/arcentra/blob/main/api/README_zh_CN.md)

gRPC API definitions for the Arcentra and Agent interaction, defined using Protocol Buffers and managed through Buf.

## Overview

This directory contains all gRPC API definitions for the Arcentra and Agent interaction, divided into five main service modules:

- **Agent Service** - Core interface for communication between Agent and Server
- **Gateway Service** - Data-plane ingestion for logs and events
- **Pipeline Service** - Pipeline management interface
- **StepRun Service** - StepRun (Step execution) management interface
- **Stream Service** - Real-time data streaming interface

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
├── gateway/v1/                 # Gateway Service API
│   ├── gateway.proto           # Proto definition file
│   ├── gateway.pb.go           # Generated Go message code
│   └── gateway_grpc.pb.go      # Generated gRPC service code
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
```

## API Service Description

### 1. Agent Service (`agent/v1`)

The main interface for communication between Agent and Server, responsible for Agent lifecycle management and step run execution.

**Main Features:**
- **Heartbeat** (`Heartbeat`) - Agent periodically sends heartbeat to Server
- **Agent Registration/Unregistration** (`Register`/`Unregister`) - Agent lifecycle management
- **StepRun Fetching** (`FetchStepRun`) - Agent actively pulls step runs to execute
- **Status Reporting** (`ReportStepRunStatus`) - Report step run execution status
- **StepRun Cancellation** (`CancelStepRun`) - Server notifies Agent to cancel step run
- **Label Updates** (`UpdateLabels`) - Dynamically update Agent's labels
- **Control Plane** (`Connect`) - Bidirectional control channel between Agent and Gateway

**Core Features:**
- Support label selector for intelligent step run routing
- Control-plane task dispatch, cancel and status feedback
- Metrics reported with task status updates

### 2. Gateway Service (`gateway/v1`)

Data-plane ingestion interface for logs and events.

**Main Features:**
- **Push Logs** (`PushLogs`) - High-throughput, batched, lossy log stream
- **Push Events** (`PushEvents`) - Reliable, idempotent event stream with retry

**Core Features:**
- Supports batching and compression
- Event idempotency with partial acceptance and retry

### 3. Pipeline Service (`pipeline/v1`)

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

### 4. StepRun Service (`steprun/v1`)

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

### 5. Stream Service (`stream/v1`)

Real-time data streaming interface, providing bidirectional streaming communication capability.

**Main Features:**
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
// Client: Receive step run status in real-time
func streamStepRunStatus(client streamv1.StreamServiceClient, stepRunID string) {
    req := &streamv1.StreamStepRunStatusRequest{
        StepRunIds: []string{stepRunID},
    }
    
    stream, err := client.StreamStepRunStatus(context.Background(), req)
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
        log.Printf("[%s] status=%v", resp.StepRunId, resp.Status)
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

### 1. How to handle large log/event payloads?

Use Gateway Service ingestion:
- Batch data in `PushLogs`/`PushEvents`
- Enable compression and set `raw_size` for verification

### 2. How to handle long-running step runs?

Use Stream Service's streaming interface:
- Real-time push step run status updates
- Use `AgentChannel` or `Connect` to maintain control-plane communication

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
