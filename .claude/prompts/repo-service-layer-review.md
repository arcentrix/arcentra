# Repository / Service Layer Review

## Purpose

审查 PR 是否正确实现 Repository / Service 分层，包括接口设计、GORM 使用、事务边界、领域查询语义。

## Context

完整分层规范见 `.claude/context/control-plane-agent.md`「控制平面分层」章节。涉及代码：

- 模型：`internal/control/model/`
- 仓储：`internal/control/repo/`
- 服务：`internal/control/service/`
- 编码规范：`.claude/context/go-coding-standards.md`「Repository / Service / Router 边界」

## Task

### 1. Model 层

- 是否嵌入 `BaseModel`（`ID` / `CreatedAt` / `UpdatedAt`）？
- 表名是否以 `t_` 开头并实现 `TableName() string`？
- 业务唯一标识列是否为 `xxx_id varchar(64)` 并加唯一索引？
- 是否使用 `is_enabled tinyint(1)` / `is_deleted tinyint(1)` 软删除？
- 字符集 / 排序：`utf8mb4` / `utf8mb4_0900_ai_ci`？
- 列上是否有合理索引（外键列、查询列、排序列）？
- 字段是否提供 GORM 标签（`gorm:"column:xxx;type:varchar(64);not null;index"`）？
- JSON 字段是否用 `datatypes.JSON` + `sonic`？

### 2. Repository 接口

- 接口名是否 `I` 开头（`IPipelineRepository` 等）？
- 是否避免 `ByID(ctx, id uint64)` 这种按主键暴露的接口？
- 是否统一按业务 ID 查询（`GetByPipelineID(ctx, pipelineID string)`）？
- 是否所有方法第一个参数都是 `context.Context`？
- 是否避免重复对接口（`ByID` 与 `ByXxxID` 同时存在）？
- 接口方法返回值是否使用领域模型，不暴露 `*gorm.DB` / `[]map[string]any`？
- 是否注册到聚合结构 `repo.Repositories` 并通过 Wire 注入？

### 3. Repository 实现

- 是否禁止 `SELECT *`，全部走 `Select("col1, col2, ...")` 明确字段？
- 是否禁止裸 SQL（`Raw` / `Exec`），除非有合理理由（迁移、报表）？
- 查询是否使用 `Where(...)` 参数化，不拼接字符串避免 SQL 注入？
- 软删除是否带 `Where("is_deleted = ?", 0)` 过滤？
- 是否处理 `gorm.ErrRecordNotFound` 并映射为领域错误（如 `ErrPipelineNotFound`）？
- 批量操作是否分批（`CreateInBatches`），避免一次 N 万行？
- 是否在 DB 层避免业务规则（不要在 SQL 里写业务逻辑）？

### 4. 事务边界

- 跨表事务是否通过 `repo.Tx(ctx, func(txRepo *Repositories) error { ... })` 封装？
- 是否避免在 service 层直接 `db.Transaction(...)`？
- 事务内是否避免外部 IO（HTTP / Kafka / gRPC），如果需要要异步触发（outbox）？

### 5. Service 层

- service 是否只持有 `*repo.Repositories` 与 `pkg/*` 客户端，不直接持有 `*gorm.DB`？
- 业务日志是否在 service 层打印（不在 router / handler）？
- 是否依赖业务 ID 作为入参，禁止接收数据库主键？
- 是否抛出领域错误（`errors.New("...")` + `errors.Is`），不抛字符串？
- 跨 service 调用是否走接口（`IXxxService`），便于 mock 测试？

### 6. 测试

- Repository 是否有单测（可用 `sqlite::memory:` + GORM 或 mockgen）？
- Service 是否有单测，mock `IXxxRepository`？
- 表驱动用例覆盖正常路径 + 主要错误分支？

## Constraints

必须遵守：

- 所有接口以 `I` 前缀
- 所有 Repository 方法第一个参数是 `context.Context`
- 业务唯一标识使用 `xxx_id` 列存 UUID 字符串
- 表名 `t_xxx`
- GORM 明确字段，禁止 `SELECT *`
- 软删除 `is_enabled` / `is_deleted` 字段

禁止：

- service 层直接使用 `*gorm.DB`
- `pkg/` 中直接使用 `database/sql` 或 `gorm`
- 暴露按主键 `id` 查询的接口方法
- 在 SQL 里写业务规则
- 事务内调用外部 IO

## Output Format

1. 总体结论
2. Model 层问题（按文件:行号）
3. Repository 接口问题
4. Repository 实现问题
5. 事务边界问题
6. Service 层问题
7. 测试覆盖建议
8. 推荐 patch / 代码示例

## Checklist

- [ ] Model 嵌入 `BaseModel`，表名 `t_xxx`，含 `TableName()`
- [ ] 业务 ID 字段 `xxx_id` 带唯一索引
- [ ] 接口 `I` 前缀，方法接收 `context.Context`
- [ ] 接口按业务 ID 暴露，无 `ByID(uint64)`
- [ ] Repository 明确 `Select`，无 `SELECT *`
- [ ] 不在 `pkg/` 直接用 GORM
- [ ] Service 不持有 `*gorm.DB`
- [ ] 事务通过 `repo.Tx` 封装
- [ ] 单元测试覆盖
