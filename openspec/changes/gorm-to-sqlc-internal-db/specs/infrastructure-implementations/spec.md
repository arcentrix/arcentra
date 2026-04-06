## MODIFIED Requirements

### 需求:仓储实现按上下文组织

领域仓储接口的实现必须放在 `internal/infra/persistence/<context>/` 下。每个实现文件必须包含仓储实现结构体及与领域模型之间的映射逻辑；数据访问必须通过 `internal/dal/queries` 中由 sqlc 生成的方法执行，或在未来归档阶段允许的极少数过渡路径中经 `database/sql` 显式执行已审核的 SQL。仓储实现必须引用 `internal/domain/<context>/` 中定义的接口。

#### 场景:Identity 仓储实现

- **当** 查看 `internal/infra/persistence/identity/`
- **那么** 必须包含 `user_repo.go`（实现 `domain/identity.IUserRepository`）、`role_repo.go`（实现 `domain/identity.IRoleRepository`）等文件；每个文件必须包含领域映射（如 `ToDomain()`/`FromDomain()` 或行模型到领域的转换），并通过 sqlc 生成查询访问数据库

#### 场景:仓储实现满足接口约束

- **当** 编译 `internal/infra/persistence/identity/` 包
- **那么** 所有导出的仓储结构体必须通过 `var _ domain.IUserRepository = (*UserRepo)(nil)` 方式显式声明接口实现

### 需求:持久化模型与领域模型的映射

`internal/infra/persistence/<context>/` 下必须提供持久化表示与领域模型之间的双向转换。转换方法必须为持久化包内可测试的函数或方法（例如 `ToDomain()` 返回领域模型，`FromDomain()` 接受领域模型并产出写入所需的参数或行结构）。持久化表示可以使用 sqlc 生成的类型、本地 PO struct，或二者组合，但**禁止**将 GORM 链式 API 作为默认查询机制。

#### 场景:Pipeline 模型转换

- **当** 从数据库查询 Pipeline 记录
- **那么** 仓储实现必须调用 sqlc 生成的方法取得行数据，再映射为领域模型返回；写入时必须将领域模型转换为 SQL 参数或 upsert 输入并调用对应 sqlc 生成方法完成持久化
