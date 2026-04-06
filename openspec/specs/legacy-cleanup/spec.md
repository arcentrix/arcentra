# legacy-cleanup 规范

## 目的
待定 - 由归档变更 finalize-ddd-adapter-wiring 创建。归档后请更新目的。
## 需求
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

