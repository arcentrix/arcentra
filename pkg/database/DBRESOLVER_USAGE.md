# GORM DBResolver 使用指南

本文档说明如何在 Arcentra 项目中使用 GORM DBResolver 实现多数据源和读写分离。

## 功能特性

- ✅ 支持多个 primary（主库）和 replicas（从库）
- ✅ 自动读写分离
- ✅ 根据表/struct 自动切换连接
- ✅ 手动切换连接
- ✅ Primary/Replicas 负载均衡
- ✅ 适用于原生 SQL
- ✅ 事务支持

## 配置示例

### 基础配置（单数据库，无读写分离）

```toml
[database]
# 公共配置
output = true
maxOpenConns = 500
maxIdleConns = 5
maxLifeTime = 300
maxIdleTime = 60

# MySQL 数据源配置
[database.mysql]
host = "127.0.0.1"
port = "3306"
user = "root"
password = "password"
dbname = "Arcentra"
```

### 读写分离配置

```toml
[database]
# 公共配置
output = true
maxOpenConns = 500
maxIdleConns = 5
maxLifeTime = 300
maxIdleTime = 60

# MySQL 数据源配置
[database.mysql]
host = "127.0.0.1"  # 默认主库（如果 primary 为空则使用此配置）
port = "3306"
user = "root"
password = "password"
dbname = "Arcentra"

# 配置多个主库（primary）
[[database.mysql.primary]]
host = "127.0.0.1"
port = "3306"
user = "root"
password = "password"
dbname = "Arcentra"

[[database.mysql.primary]]
host = "127.0.0.2"
port = "3306"
user = "root"
password = "password"
dbname = "Arcentra"

# 配置多个从库（replicas）
[[database.mysql.replicas]]
host = "127.0.0.3"
port = "3306"
user = "readonly"
password = "password"
dbname = "Arcentra"

[[database.mysql.replicas]]
host = "127.0.0.4"
port = "3306"
user = "readonly"
password = "password"
dbname = "Arcentra"
```

## 自动读写分离

DBResolver 会根据操作类型自动选择连接：

- **查询操作**（SELECT）：自动使用 replicas（从库）
- **写操作**（INSERT/UPDATE/DELETE）：自动使用 primary（主库）

### 示例

```go
// 自动使用 replicas（从库）
db.Find(&users)                    // SELECT 查询
db.Table("users").Rows()          // 查询操作
db.Raw("SELECT * FROM users").Row() // 原生 SQL 查询

// 自动使用 primary（主库）
db.Create(&user)                   // INSERT
db.Update("name", "jinzhu")       // UPDATE
db.Delete(&user)                   // DELETE
db.Exec("UPDATE users SET name = ?", "jinzhu") // 原生 SQL 写操作
```

## 手动切换连接

如果需要强制指定使用主库或从库，可以使用辅助函数：

### 使用辅助函数

```go
import "github.com/arcentrix/arcentra/pkg/database"

// 强制使用主库（primary）
db.Clauses(database.Write()).First(&user)

// 强制使用从库（replicas）
db.Clauses(database.Read()).Find(&users)

// 使用便捷方法
readDB := database.ReadDB(db)
readDB.Find(&users)

writeDB := database.WriteDB(db)
writeDB.Create(&user)
```

### 使用 GORM 原生方式

```go
import "gorm.io/plugin/dbresolver"

// 强制使用主库
db.Clauses(dbresolver.Write).First(&user)

// 强制使用从库
db.Clauses(dbresolver.Read).Find(&users)
```

## 事务支持

使用事务时，DBResolver 会保持使用同一个连接，不会切换 primary/replicas。

```go
// 基于默认从库开始事务
tx := db.Clauses(dbresolver.Read).Begin()

// 基于默认主库开始事务
tx := db.Clauses(dbresolver.Write).Begin()

// 在事务中执行操作
tx.Create(&user)
tx.Commit()
```

## 命名 Resolver（高级用法）

如果需要为不同的表或 struct 配置不同的 resolver，可以在注册时指定：

```go
// 注册名为 "secondary" 的 resolver，用于 orders 表和 Product struct
db.Use(dbresolver.Register(dbresolver.Config{
    Sources:  []gorm.Dialector{mysql.Open("db6_dsn"), mysql.Open("db7_dsn")},
    Replicas: []gorm.Dialector{mysql.Open("db8_dsn")},
}, "orders", &Product{}, "secondary"))
```

使用命名 resolver：

```go
// 使用 "secondary" resolver 的从库
db.Clauses(dbresolver.Use("secondary")).Find(&orders)

// 使用 "secondary" resolver 的主库
db.Clauses(dbresolver.Use("secondary"), dbresolver.Write).Find(&orders)

// 或使用便捷方法
database.UseResolver(db, "secondary").Find(&orders)
database.UseResolverWrite(db, "secondary").Create(&order)
```

## 负载均衡策略

DBResolver 支持多种负载均衡策略：

- `RandomPolicy{}`：随机选择（默认）
- `RoundRobinPolicy()`：轮询
- `StrictRoundRobinPolicy()`：严格轮询

配置策略：

```go
db.Use(dbresolver.Register(dbresolver.Config{
    Sources:  []gorm.Dialector{mysql.Open("db1_dsn")},
    Replicas: []gorm.Dialector{mysql.Open("db2_dsn"), mysql.Open("db3_dsn")},
    Policy:   dbresolver.RandomPolicy{}, // 或 RoundRobinPolicy()
}))
```

## 连接池配置

连接池配置会自动应用到所有 primary 和 replicas：

```go
db.Use(dbresolver.Register(dbresolver.Config{
    Sources:  []gorm.Dialector{mysql.Open("dsn1")},
    Replicas: []gorm.Dialector{mysql.Open("dsn2")},
}).
    SetConnMaxIdleTime(time.Hour).
    SetConnMaxLifetime(24 * time.Hour).
    SetMaxIdleConns(100).
    SetMaxOpenConns(200))
```

## 注意事项

1. **向后兼容**：如果不配置 `primary` 和 `replicas`，系统会使用传统的单数据库连接方式，完全向后兼容。

2. **数据一致性**：确保所有 primary 和 replicas 的数据结构一致，避免查询错误。

3. **主从延迟**：读写分离时，从库可能存在延迟，对于强一致性要求的场景，建议强制使用主库读取。

4. **事务边界**：事务中的所有操作都会使用同一个连接，不会自动切换。

5. **原生 SQL**：对于原生 SQL，DBResolver 会从 SQL 中提取表名来匹配 Resolver。如果 SQL 以 `SELECT` 开头（`SELECT FOR UPDATE` 除外），会使用 replicas，否则使用 primary。

## 参考文档

- [GORM DBResolver 官方文档](https://gorm.io/zh_CN/docs/dbresolver.html)
- [GORM 官方文档](https://gorm.io/zh_CN/docs/)

