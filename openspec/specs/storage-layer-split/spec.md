# storage-layer-split 规范

## 目的
待定 - 由归档变更 refactor-internal-pkg-ddd-clean 创建。归档后请更新目的。
## 需求
### 需求:IStorage 接口定义在领域层

`IStorage` 接口必须定义在 `internal/domain/agent/repository.go` 中（与现有 `IStorageRepository` 并列），作为存储能力的领域端口。

#### 场景:IStorage 定义位置
- **当** 查找 `IStorage` 接口的定义文件
- **那么** 它位于 `internal/domain/agent/` 目录下

#### 场景:IStorage 接口内容完整
- **当** 读取领域层的 `IStorage` 接口
- **那么** 它包含 Upload、Download、Delete、List 等存储操作方法签名，与当前 `internal/pkg/storage/storage_interface.go` 中的定义一致

### 需求:云存储实现位于基础设施层

S3Storage、OSSStorage、MinioStorage、GCSStorage、COSStorage 的具体实现必须位于 `internal/infra/storage/` 包中。

#### 场景:S3 实现位于 infra
- **当** 查找 `S3Storage` 结构体的定义
- **那么** 它位于 `internal/infra/storage/` 目录下

#### 场景:所有云存储实现位于 infra
- **当** 查找所有实现了 `IStorage` 接口的结构体
- **那么** 它们均位于 `internal/infra/storage/` 目录下

### 需求:DbProvider 运行时切换位于基础设施层

`DbProvider`（通过 `IStorageRepository` 动态读取存储配置并构造 `IStorage` 实例的运行时管理器）必须位于 `internal/infra/storage/` 包中。

#### 场景:DbProvider 位置
- **当** 查找 `DbProvider` 结构体（或重命名后的等效类型）
- **那么** 它位于 `internal/infra/storage/` 目录下

#### 场景:DbProvider 依赖领域接口
- **当** 检查 `DbProvider` 的依赖
- **那么** 它通过 `internal/domain/agent.IStorageRepository` 获取配置，通过 `internal/domain/agent.IStorage` 接口暴露能力

### 需求:storage Wire ProviderSet 移至 infra 层

`ProvideStorageFromDB` 和 `ProviderSet` 必须从 `internal/pkg/storage/provider.go` 移至 `internal/infra/storage/provider.go`。

#### 场景:ProviderSet 位置
- **当** 查找 storage 的 `ProviderSet` 定义
- **那么** 它位于 `internal/infra/storage/provider.go`

#### 场景:internal/pkg/storage 不再存在
- **当** 检查 `internal/pkg/storage/` 目录
- **那么** 该目录不存在或为空（所有文件已迁移）

