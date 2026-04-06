## ADDED Requirements

### 需求:存储适配器必须桥接 pkg/storage 到 domain 接口

`internal/infra/storage/` 必须提供适配器，将 `internal/pkg/storage.IStorage` 实现包装为 `internal/domain/agent.IStorageRepository`（或等效的 domain 层存储接口）。

#### 场景:S3 存储适配
- **当** 配置为 S3 存储后端
- **那么** `infra/storage` 适配器必须将 `storage.S3Storage` 的能力转换为 domain 层接口方法，包括上传、下载、删除、列表等操作

#### 场景:MinIO 存储适配
- **当** 配置为 MinIO 存储后端
- **那么** `infra/storage` 适配器必须正确适配 `storage.MinioStorage`

#### 场景:OSS/COS/GCS 存储适配
- **当** 配置为 OSS、COS 或 GCS 存储后端
- **那么** `infra/storage` 适配器必须分别正确适配对应的实现

### 需求:存储 ProviderSet 必须绑定 domain 接口

`internal/infra/storage/provider.go` 必须通过 `wire.Bind` 将适配器实现绑定到 domain 层存储接口，使 case 层可以通过接口注入使用。

#### 场景:Wire 解析存储依赖
- **当** `wire gen` 被执行
- **那么** `infra/storage.ProviderSet` 必须能提供 domain 层存储接口的实现

### 需求:pkg/storage 包保持不变

`internal/pkg/storage/` 的代码必须保持不变（因为 Agent 端也使用），仅在 `infra/storage/` 创建适配层。

#### 场景:Agent 端不受影响
- **当** 存储适配器被添加到 `infra/storage/`
- **那么** `internal/agent/` 中对 `internal/pkg/storage` 的引用必须不受影响
