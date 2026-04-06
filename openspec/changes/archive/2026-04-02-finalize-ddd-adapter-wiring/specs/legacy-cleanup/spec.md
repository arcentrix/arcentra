## REMOVED Requirements

### 需求:删除 internal/control/ 目录
**Reason**: 所有业务逻辑已迁移到 `internal/domain/`、`internal/case/`、`internal/adapter/`、`internal/infra/` 四层架构中，`internal/control/` 不再有活跃引用。
**Migration**: 路由迁到 `internal/adapter/http/`，服务迁到 `internal/case/`，仓储迁到 `internal/infra/persistence/`，模型迁到 `internal/domain/`。

#### 场景:control 目录删除
- **当** 所有适配器迁移和 Wire 切换完成
- **那么** 必须删除 `internal/control/` 下的 router/、service/、repo/、model/、config/、consts/ 目录（bootstrap/ 视决策 3 保留或重构）

#### 场景:无残留引用
- **当** `internal/control/` 被删除
- **那么** 执行 `grep -r "internal/control" --include="*.go"` 必须无匹配结果（排除 `internal/control/bootstrap/` 如果保留）

### 需求:删除 internal/pkg/grpc/ 目录
**Reason**: gRPC 服务实现和拦截器已迁移到 `internal/adapter/grpc/`，`internal/pkg/grpc/` 不再有活跃引用。
**Migration**: ServerWrapper 迁到 `internal/adapter/grpc/server.go`，拦截器迁到 `internal/adapter/grpc/interceptor/`，gRPC 服务实现迁到 `internal/adapter/grpc/service_*.go`。

#### 场景:pkg/grpc 目录删除
- **当** `adapter/grpc/` 的完整实现就绪且 Wire 已切换
- **那么** 必须删除 `internal/pkg/grpc/` 整个目录

## ADDED Requirements

### 需求:删除后必须通过完整验证

删除旧代码后必须通过编译和测试验证。

#### 场景:编译验证
- **当** 旧代码删除完成
- **那么** 执行 `go build ./...` 必须成功，无编译错误

#### 场景:测试验证
- **当** 旧代码删除完成
- **那么** 执行 `go test ./...` 必须通过（允许已知的外部依赖跳过）

#### 场景:CLI 二进制验证
- **当** 旧代码删除完成
- **那么** `go build ./cmd/arcentra`、`go build ./cmd/arcentra-agent`、`go build ./cmd/cli` 必须全部成功
