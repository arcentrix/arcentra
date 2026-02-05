# Sandbox Package

Sandbox 包提供了沙箱执行环境的抽象接口和 containerd 实现，用于在隔离的容器环境中执行任务。

## 功能特性

- **容器管理**：创建、启动、停止、删除容器
- **命令执行**：在容器中执行命令并获取结果
- **网络配置**：支持 bridge、host、none 网络模式
- **资源限制**：支持 CPU 和内存限制配置
- **日志收集**：支持容器日志收集
- **自动清理**：支持资源自动清理

## 接口定义

### Sandbox 接口

```go
type Sandbox interface {
    Create(ctx context.Context, opts *CreateOptions) (string, error)
    Start(ctx context.Context, containerID string) error
    Execute(ctx context.Context, containerID string, cmd []string, opts *ExecuteOptions) (*ExecuteResult, error)
    Stop(ctx context.Context, containerID string, timeout time.Duration) error
    Remove(ctx context.Context, containerID string) error
    GetLogs(ctx context.Context, containerID string, opts *LogOptions) (io.ReadCloser, error)
    Cleanup(ctx context.Context) error
    Close() error
}
```

## 使用示例

### 1. 从配置创建沙箱

```go
import (
    "github.com/arcentrix/arcentra/internal/agent/config"
    "github.com/arcentrix/arcentra/pkg/sandbox"
    "github.com/arcentrix/arcentra/pkg/log"
)

cfg := config.NewConf("conf.d/agent.toml")
logger := log.Logger{Log: log.GetLogger()}

sb, err := sandbox.NewSandboxFromConfig(cfg, logger)
if err != nil {
    // 处理错误
}
defer sb.Close()
```

### 2. 直接创建 Containerd 沙箱

```go
import (
    "github.com/arcentrix/arcentra/pkg/sandbox"
    "github.com/arcentrix/arcentra/pkg/log"
)

config := &sandbox.ContainerdConfig{
    UnixSocket:  "/run/containerd/containerd.sock",
    Namespace:   "arcentra",
    DefaultImage: "alpine:latest",
    NetworkMode: "bridge",
    Resources: &sandbox.Resources{
        CPU:    "1",
        Memory: "1G",
    },
}

sb, err := sandbox.NewContainerdSandbox(config, logger)
if err != nil {
    // 处理错误
}
defer sb.Close()
```

### 3. 创建容器

```go
ctx := context.Background()

opts := &sandbox.CreateOptions{
    Image:      "alpine:latest",
    Command:    []string{"sh"},
    Args:       []string{"-c", "echo hello"},
    NetworkMode: "bridge",
    Resources: &sandbox.Resources{
        CPU:    "0.5",
        Memory: "512M",
    },
    Env: map[string]string{
        "ENV_VAR": "value",
    },
    WorkingDir: "/workspace",
    Mounts: []sandbox.Mount{
        {
            Source:   "/host/path",
            Target:   "/container/path",
            Type:     "bind",
            ReadOnly: false,
        },
    },
}

containerID, err := sb.Create(ctx, opts)
if err != nil {
    // 处理错误
}
```

### 4. 执行命令

```go
executeOpts := &sandbox.ExecuteOptions{
    Env: map[string]string{
        "VAR": "value",
    },
    WorkingDir: "/workspace",
    Timeout:    30 * time.Second,
}

result := sb.Execute(ctx, containerID, []string{"echo", "Hello World"}, executeOpts)
if result.ExitCode != 0 {
    // 处理错误
}

fmt.Printf("Output: %s\n", result.Stdout)
fmt.Printf("Exit Code: %d\n", result.ExitCode)
fmt.Printf("Duration: %v\n", result.Duration)
```

### 5. 停止和删除容器

```go
// 停止容器（优雅关闭，超时 10 秒）
err := sb.Stop(ctx, containerID, 10*time.Second)
if err != nil {
    // 处理错误
}

// 删除容器
err = sb.Remove(ctx, containerID)
if err != nil {
    // 处理错误
}
```

### 6. 获取日志

```go
logOpts := &sandbox.LogOptions{
    Follow:     false,
    Tail:       100,
    Timestamps: true,
}

logs, err := sb.GetLogs(ctx, containerID, logOpts)
if err != nil {
    // 处理错误
}
defer logs.Close()

// 读取日志
io.Copy(os.Stdout, logs)
```

## 配置说明

### ContainerdConfig

- `UnixSocket`: containerd unix socket 路径（默认：`/run/containerd/containerd.sock`）
- `Namespace`: containerd 命名空间（默认：`Arcentra`）
- `DefaultImage`: 默认容器镜像
- `NetworkMode`: 默认网络模式（bridge, host, none）
- `Resources`: 默认资源限制

### CreateOptions

- `Image`: 容器镜像（必需）
- `Command`: 命令（必需）
- `Args`: 命令参数
- `Env`: 环境变量
- `WorkingDir`: 工作目录
- `NetworkMode`: 网络模式（bridge, host, none）
- `Resources`: 资源限制
- `Mounts`: 挂载点
- `Labels`: 容器标签
- `Hostname`: 主机名
- `Privileged`: 是否启用特权模式

### Resources

- `CPU`: CPU 限制（例如：`"1"`, `"0.5"`, `"1000m"`）
- `Memory`: 内存限制（例如：`"1G"`, `"512M"`, `"1024m"`）
- `CPUShares`: CPU 权重
- `MemoryReservation`: 内存预留

### ExecuteOptions

- `Env`: 环境变量
- `WorkingDir`: 工作目录
- `User`: 运行用户（例如：`"root"`, `"1000:1000"`）
- `TTY`: 是否启用 TTY
- `Stdin`: 标准输入
- `Stdout`: 标准输出
- `Stderr`: 标准错误
- `Timeout`: 执行超时时间

## 网络模式

- **bridge**: 桥接网络（默认），容器有独立的网络命名空间
- **host**: 主机网络，容器共享主机网络命名空间
- **none**: 无网络，容器没有网络接口

## 资源限制格式

### CPU

- `"1"`: 1 个 CPU 核心
- `"0.5"`: 0.5 个 CPU 核心
- `"1000m"`: 1000 毫核（1 个核心）

### Memory

- `"1G"`: 1 GB
- `"512M"`: 512 MB
- `"1024K"`: 1024 KB
- `"1024"`: 1024 字节

## 依赖要求

- containerd daemon 必须运行
- containerd unix socket 必须可访问
- 需要适当的权限来创建和管理容器

## 注意事项

1. **命名空间隔离**：所有容器在指定的 containerd 命名空间中创建，便于管理和隔离
2. **资源清理**：使用 `Close()` 方法会自动清理所有资源
3. **超时处理**：执行命令时建议设置合理的超时时间
4. **日志收集**：日志收集功能依赖于 containerd 的日志配置

## 测试

运行测试：

```bash
# 运行所有测试
go test ./pkg/sandbox/... -v

# 运行集成测试（需要 containerd）
go test ./pkg/sandbox/... -v -run TestContainerdSandbox
```

注意：集成测试需要运行中的 containerd daemon。
