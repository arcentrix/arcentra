# Go 编码规范与工程准则

## 基础原则

arcentra 是 Go 1.24+ 编写的 CI/CD 控制平面，分为 `cmd/arcentra`（控制平面）与 `cmd/arcentra-agent`（分布式 Agent）两个进程，使用 Google Wire 编译时依赖注入。代码遵循以下原则：

- 用类型表达业务语义，避免到处裸用 `string` / `int64`
- 业务标识符使用 UUID 字符串（如 `pipelineID`、`agentID`、`runID`），存储于 `xxx_id` 列；数据库自增主键 `id` 不外暴露
- 错误显式 `error` 返回；除 `init` 之外禁止 `panic`
- 任何 IO / RPC / DB / 队列接口必须显式接收 `context.Context`；`ctx` 通过参数传递，不存放到结构体字段
- 公共能力优先用 interface 暴露，实现保持在内部，便于替换和测试
- 单一职责：一个文件 / 结构体 / 函数只负责一件清晰的事
- 公共逻辑沉淀到 `pkg/`，平台共享逻辑沉淀到 `internal/shared/`

## 必须通过的代码质量检查

提交前至少执行：

```bash
make fmt-check
make lint
make staticcheck
make test
```

提交前不应存在：

- gofmt diff
- `golangci-lint` warning / error（配置见 `.golangci.yml`）
- 无原因的 `panic()`、`log.Fatal()`
- 临时 `fmt.Println` / `fmt.Printf` / `println` 调试代码
- 未使用的代码、变量、import
- 直接 `go func` 启动的 goroutine

## golangci-lint 关键 linter

`.golangci.yml` 启用的关键 linter：

- `errcheck`：不得静默忽略可检查的错误返回；显式 `_ = ...` 或 `defer func() { _ = closer.Close() }()`
- `staticcheck`：遵守 Go 惯例（ST1008 error 必须最后返回；避免 SA9003 空分支）
- `unused`：不留未使用字段 / 函数 / 测试辅助
- `gosec`：注意敏感信息泄漏与不安全的随机数

## 命名规范

| 类型 | 风格 | 示例 |
|------|------|------|
| 包 | snake_case 单词 | `pipeline`, `executor`, `nova` |
| 类型 | UpperCamelCase | `PipelineRun`, `AgentRepository` |
| 函数/方法 | UpperCamelCase 公开 / lowerCamelCase 私有 | `SubmitRun`, `recalcStatus` |
| 接口 | `I` 前缀 | `IPipelineRepository`, `IAgentRepository`, `IPipelineEngine` |
| 常量 | UpperCamelCase 公开 / lowerCamelCase 私有 | `DefaultJobTimeout` |
| ID 字段 | `ID` 而非 `Id` | `PipelineID`, `RunID`, `AgentID` |

避免使用过于泛的名称：`Manager`、`Handler`、`Processor`，除非上下文非常明确（如 `taskqueue.Worker`、`pipeline.AgentManager`）。

## Interface 规范

- 所有 Repository 接口必须以 `I` 开头：`IPipelineRepository`、`IAgentRepository`、`IRunRepository` 等
- Repository 接口只暴露领域语义方法，统一用业务唯一标识（如 `pipelineID`、`agentID`）作为查询键
- 杜绝 `ByID` / `ByXXX` 成对的重复接口
- 严禁按数据库主键 `id` 暴露原始查询
- 所有接口方法必须携带 `context.Context`

## Repository / Service / Router 边界

```
router → service → repo → gorm.DB
            ↓
        process / shared
            ↓
        pkg/* （cache、mq、taskqueue、grpc、storage、plugin、cron …）
```

- `router/` 只做参数解析、鉴权、响应封装；业务逻辑禁止下沉到 handler
- `service/` 持有 `Repositories`、`pkg/*` 客户端、`IPipelineEngine` 等依赖；业务逻辑全部落在此层
- `repo/` 仅封装 GORM；只暴露领域方法，禁止把 `gorm.DB` 透传到 service
- `process/` 是控制平面的流水线编排引擎，调用 `internal/shared/pipeline` 与 `pkg/nova`
- HTTP 接口尽量在 service 层打印业务日志，避免在 controller / router 层打印

## 并发约定

- 禁止直接使用 `go` 或 `go func`，所有 goroutine 必须通过 `pkg/safe.SafelyGo` 启动
  - `SafelyGo` 内置 panic recover，上报 trace 与 metric
- 长循环 / 定时 / Worker 必须接收 `context.Context` 并响应 cancel
- 不要在结构体中存放 `context.Context`，通过参数传递

## 日志规范

项目统一使用 `pkg/log`（背后是封装的 zap）。

推荐结构化日志：

```go
log.Infow(ctx, "pipeline submitted",
    "pipelineID", pipelineID,
    "runID", runID,
    "triggerType", triggerType)
```

- HTTP 接口：service 层打印业务日志，handler / router 层不打日志
- gRPC 接口：拦截器统一打 access log，业务日志在 service 层
- 提交前删除：`fmt.Println`、`fmt.Printf`、`println`、`log.Println`
- 不要在每事件 / 每步路径上打 `Debug` 之外的日志

## JSON 与序列化

- **统一使用 `sonic`**，禁止 `encoding/json`
- JSON 字段命名使用驼峰：`json:"createdAt"`、`json:"pipelineID"`
- gRPC / Kafka 入站出站使用 protobuf（`api/**/*.proto`，生成代码在 `api/**/*.pb.go`，不可手编辑）
- 数据库层使用 GORM，JSON 列推荐 `gorm.io/datatypes.JSON` + sonic 编解码

## 错误处理规范

- 优先用 `errors.New` + `errors.Is` / `errors.As`，禁止字符串错误到处传递
- 在 `consts/` 或对应模块定义领域错误：

  ```go
  var ErrPipelineNotFound = errors.New("pipeline not found")
  var ErrRunAlreadyCompleted = errors.New("run already completed")
  ```

- service 返回 error 时附加上下文（`fmt.Errorf("submit run %s: %w", runID, err)`）
- HTTP 响应统一走 `UnifiedResponse` 中间件包装
- gRPC 服务端用 `status.Error(codes.*, ...)` 返回标准错误码

## panic 使用准则

生产代码默认不允许 `panic()`，除非满足：

- 状态属于内部不变量，不可能由外部输入触发
- 前置代码已经明确保证安全
- 或位于 `init()` / `wire_gen` 引导阶段

可以接受：

```go
if cfg == nil {
    panic("config must be loaded before bootstrap")  // 引导阶段不变量
}
```

不推荐：

```go
val, _ := parse(input)
if val == "" {
    panic("invalid input")  // 应返回 error
}
```

测试代码使用 `t.Fatal` / `t.Fatalf`，不要 `t.Errorf` 然后继续。

## 注释规范

- 所有导出标识符必须有 GoDoc 中文注释
- 注释**不要**用中文句号（。）结尾
- 注释**不要**用阿拉伯数字序号（`// 1. xxx // 2. xxx`）
- 注释解释「为什么」，不要解释「代码做了什么」

不推荐：

```go
// 删除 run
delete(runs, id)
```

推荐：

```go
// 清理已完成 run 防止 Coordinator 重复回调
delete(runs, id)
```

## 时间字段规范

- 时间字段必须明确单位与时区
- 数据库列：`timestamp`（UTC），`created_at` / `updated_at`（带 `ON UPDATE CURRENT_TIMESTAMP`）
- Go 内部：`time.Time`，避免 `int64` 时间戳
- 跨进程协议（proto / Kafka）：使用 `int64 createdAtMs` / `tsMicros`，明确单位后缀

## 测试规范

- 新增、修改函数原则上必须配单元测试
- 优先 table-driven：

  ```go
  cases := []struct{
      name string
      input  XXX
      wantErr error
  }{ ... }
  for _, tc := range cases {
      t.Run(tc.name, func(t *testing.T) { ... })
  }
  ```

- 失败必须 `t.Fatal` / `t.Fatalf`
- 测试入口跑 `go test -race -count=1 ./...`，竞争检测必须开启
- 第三方测试库（如 testify）可使用但不滥用
- 集成式测试（涉及 GORM、Kafka、gRPC）放在对应包的 `*_integration_test.go` 并用 build tag 隔离

## 代码生成边界

- `api/**/*.pb.go`、`api/**/*_grpc.pb.go`：由 `make buf` 生成，禁止手编辑
- `cmd/*/wire_gen.go`：由 `make wire` 生成，禁止手编辑
- `internal/control/repo/queries/*`（如有）：由生成器产出，禁止手编辑
- 修改 `.proto` 后必须 `make buf`；修改 `wire.go` 或新加 `ProviderSet` 后必须 `make wire`

## 模块依赖方向

依赖方向必须单向：

```
router / bootstrap
        ↓
service
        ↓
repo / process / shared
        ↓
pkg/*
        ↓
api/* (protoc 生成的 stub)
```

禁止内层反向依赖外层。`pkg/` 不允许 import `internal/`。

## Wire DI 规范

- 每个包在 `provider.go` 中定义 `ProviderSet`
- `wire.Bind(new(IXxxRepository), new(*xxxRepository))` 在 `repo` / `service` 内部完成
- 不要把 `*gorm.DB` 当作 ProviderSet 的暴露依赖直接注入到 service —— 应该注入聚合 `*repo.Repositories`
- 修改 `wire.go` 后必须执行 `make wire`，并提交 `wire_gen.go`
