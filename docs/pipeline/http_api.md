# Pipeline HTTP API 文档

设计语义与 gRPC 对齐说明见 [`api_design.md`](./api_design.md)。执行引擎与控制面触发链关系见 [`execution_inventory.md`](./execution_inventory.md)。

## 基础信息

- Base Path: `/api/v1/pipelines`
- 鉴权: `Authorization: Bearer <token>`
- Content-Type: `application/json`

## 统一响应格式

成功（有 detail）：

```json
{
  "code": 0,
  "msg": "success",
  "detail": {},
  "timestamp": 1730000000
}
```

成功（无 detail）：

```json
{
  "code": 0,
  "msg": "success",
  "timestamp": 1730000000
}
```

失败：

```json
{
  "code": 400,
  "errMsg": "xxx",
  "path": "/api/v1/pipelines/...",
  "timestamp": 1730000000
}
```

## 枚举约定

- `saveMode`: `direct` | `pr`
- `format`: `json` | `yaml` | `yml`
- `status`: `pending` | `running` | `success` | `failed` | `cancelled` | `paused`

## API 列表

### Pipeline 管理

- `POST /api/v1/pipelines`
- `PUT /api/v1/pipelines/:pipelineId`
- `GET /api/v1/pipelines/:pipelineId`
- `GET /api/v1/pipelines`
- `DELETE /api/v1/pipelines/:pipelineId`

### 定义文件

- `GET /api/v1/pipelines/:pipelineId/spec`
- `POST /api/v1/pipelines/:pipelineId/spec/validate`
- `POST /api/v1/pipelines/:pipelineId/spec/save`

### 运行控制

- `POST /api/v1/pipelines/:pipelineId/trigger`
- `GET /api/v1/pipelines/:pipelineId/runs`
- `GET /api/v1/pipelines/runs/:runId`
- `POST /api/v1/pipelines/:pipelineId/runs/:runId/stop`
- `POST /api/v1/pipelines/:pipelineId/runs/:runId/pause`
- `POST /api/v1/pipelines/:pipelineId/runs/:runId/resume`

## 关键请求体字段

### CreatePipeline

```json
{
  "projectId": "proj_xxx",
  "name": "build-main",
  "description": "main branch build",
  "repoUrl": "https://github.com/org/repo.git",
  "defaultBranch": "main",
  "pipelineFilePath": ".arcentra/pipeline.yaml",
  "saveMode": "pr",
  "prTargetBranch": "main",
  "metadata": {
    "owner": "devops"
  },
  "createdBy": "user_xxx"
}
```

### ValidatePipelineDefinition

```json
{
  "spec": {
    "namespace": "demo.pipeline",
    "version": "v1",
    "jobs": [
      {
        "name": "build",
        "steps": [
          {
            "name": "compile",
            "uses": "actions/go-build@v1",
            "args": {
              "go_version": "1.23"
            }
          }
        ]
      }
    ]
  },
  "format": "yaml"
}
```

### SavePipelineDefinition

```json
{
  "spec": {
    "namespace": "demo.pipeline",
    "version": "v1",
    "jobs": [
      {
        "name": "build",
        "steps": [
          {
            "name": "compile",
            "uses": "actions/go-build@v1",
            "args": {
              "go_version": "1.23"
            }
          }
        ]
      }
    ]
  },
  "format": "yaml",
  "expectedHeadCommitSha": "abc123",
  "commitMessage": "update pipeline",
  "requestId": "req-20260302-001",
  "editor": "user_xxx"
}
```

> 注意：`content` 文本字段已移除，定义相关接口仅接受结构化 `spec`。

### TriggerPipeline

```json
{
  "variables": {
    "env": "prod"
  },
  "triggeredBy": "user_xxx",
  "requestId": "trigger-001"
}
```

### Pause/Resume/Stop

```json
{
  "reason": "manual operation",
  "operator": "user_xxx"
}
```
