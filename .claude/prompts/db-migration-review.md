# DB Schema / Migration / GORM Review

## Purpose

审查 PR 是否正确实现 / 修改 MySQL 表结构、迁移脚本、GORM 模型。

## Context

- 数据库：MySQL 8（InnoDB，`utf8mb4` / `utf8mb4_0900_ai_ci`）
- 模型：`internal/control/model/`
- 迁移脚本：`docs/migrations/`（按 `NNN_description.sql` 命名）
- 通用字段：`id`（自增主键）/ `xxx_id`（UUID 业务键）/ `is_enabled` / `is_deleted` / `created_at` / `updated_at`
- 完整约定见 `.cursor/rules/arcentra-rule.mdc` 和 `.claude/context/control-plane-agent.md`

## Task

### 1. 表命名 / 字段

- 表名是否 `t_xxx`？
- 字符集 / 排序：`utf8mb4` / `utf8mb4_0900_ai_ci`？
- 引擎：`InnoDB`？
- 是否包含：`id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY`、`xxx_id VARCHAR(64) NOT NULL UNIQUE`、`is_enabled TINYINT(1) NOT NULL DEFAULT 1`、`is_deleted TINYINT(1) NOT NULL DEFAULT 0`、`created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP`、`updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP`？
- 业务唯一标识列（`xxx_id`）类型是否为 `VARCHAR(64)`（UUID 长度足够）？

### 2. 列设计

- 字段类型是否合理（`VARCHAR(255)` 默认上限 / `TEXT` 长文本 / `JSON` 结构化数据 / `BIGINT` 时间戳 / `TIMESTAMP` 时间）？
- `NOT NULL` 与默认值是否明确？
- 字符串列长度是否合理（避免 `VARCHAR(2000)` 当 key 用）？
- enum 类列是否用 `TINYINT` + Go 常量映射，避免 SQL ENUM 类型（迁移困难）？
- 金额 / 数值字段是否避免 `DOUBLE`（精度问题），改用 `DECIMAL` 或定点 `BIGINT`？

### 3. 索引

- 业务唯一标识 `xxx_id` 是否有 `UNIQUE INDEX`？
- 高频查询列是否有索引？
- 是否避免重复索引（已有 `(a, b)` 联合索引时单独再建 `(a)` 可能多余）？
- 是否避免在 `is_deleted` 单列建索引（区分度低）？
- 复合索引顺序是否最常用列在前（最左前缀）？
- 是否考虑 covering index 减少回表？

### 4. 外键

- arcentra 默认 **不使用** 物理外键（应用层保证一致性）
- 如果新增物理外键需求，必须在 PR 描述中说明理由

### 5. 迁移脚本

- 文件名是否 `NNN_description.sql` 顺序递增？
- 是否幂等（重复执行不报错 —— `IF NOT EXISTS` / `IF EXISTS`）？
- 是否分步：`ALTER TABLE ADD COLUMN` → 数据回填 → `ALTER TABLE ADD INDEX` → `ALTER TABLE DROP COLUMN`？
- 大表 `ALTER TABLE` 是否考虑在线 DDL（`ALGORITHM=INPLACE, LOCK=NONE`）或 gh-ost / pt-osc？
- 是否提供回滚脚本（`down.sql`）或文档说明回滚步骤？
- 是否包含必要的 `INSERT` 初始化数据（用 `INSERT … SELECT` 或 `INSERT IGNORE`）？

### 6. GORM 模型

- 是否嵌入 `BaseModel`？
- 是否实现 `TableName() string` 返回 `t_xxx`？
- 字段 tag 是否齐全：`gorm:"column:xxx_id;type:varchar(64);not null;uniqueIndex"`？
- 时间字段是否 `time.Time` 类型（而非 `int64`）？
- JSON 列是否用 `datatypes.JSON` 并配 sonic 编解码？
- 软删除是否禁用 GORM 默认 `DeletedAt`（项目用自定义 `is_deleted`）？

### 7. Repository 适配

- 新增 / 修改字段后 Repository 是否同步更新 `Select(...)`、`Updates(map[string]any{...})`？
- 查询是否带 `WHERE is_deleted = 0`？
- 是否避免 `db.Model(&X{}).Updates(struct{...})` 把 0/空字符串写回去（应该用 map 或 select 模式）？

### 8. 兼容性

- 是否考虑灰度发布期间 新旧代码 / 新旧表结构 并存？
- 删除字段：是否分两步 —— 先停止代码引用 → 下一版本删字段？
- 修改字段类型：是否先加新列回填、切流量、删旧列？

### 9. 性能

- 大表新增非空列是否提供默认值（否则触发全表重写）？
- 大表加索引是否估算时间窗口？
- 是否避免在迁移中跑大事务（按批 `UPDATE … LIMIT 1000`）？

### 10. 安全

- 是否避免在迁移脚本中包含密码 / token 明文？
- 是否避免 `GRANT` / `CREATE USER` 之类 DBA 操作（应该走 DBA 流程）？

## Constraints

必须遵守：

- 表名 `t_xxx`
- 字符集 `utf8mb4_0900_ai_ci`
- 通用字段（id / xxx_id / is_enabled / is_deleted / created_at / updated_at）齐全
- 业务唯一标识 `xxx_id` `VARCHAR(64)` 唯一索引
- 迁移脚本幂等 + 顺序命名
- GORM 模型实现 `TableName()` + 嵌入 `BaseModel`
- 软删除按 `is_deleted` 过滤

禁止：

- `SELECT *` / 裸 SQL
- 使用 SQL ENUM 类型
- 物理外键（除非有强理由）
- `DOUBLE` 存金额
- 在迁移中跑超大事务
- 修改 / 删除字段不分步（不考虑灰度）
- 在 GORM 模型上启用默认 `DeletedAt` 软删除

## Output Format

1. 总体结论
2. 表结构问题（按文件:行号）
3. 索引设计问题
4. 迁移脚本问题（幂等 / 顺序 / 回滚）
5. GORM 模型问题
6. Repository 适配问题
7. 灰度 / 兼容性 风险
8. 性能风险
9. 推荐 patch / SQL 示例

## Checklist

- [ ] 表名 `t_xxx`，`utf8mb4_0900_ai_ci`
- [ ] 通用字段齐全
- [ ] `xxx_id` `VARCHAR(64)` + `UNIQUE`
- [ ] 索引合理无冗余
- [ ] 迁移脚本顺序命名 + 幂等
- [ ] 大表 DDL 考虑 in-place / 在线工具
- [ ] GORM 模型 `TableName()` + `BaseModel`
- [ ] Repository `Select` 已更新
- [ ] 灰度兼容性已考虑
