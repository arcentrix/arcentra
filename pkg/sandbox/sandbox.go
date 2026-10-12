// Copyright 2025 Arcentra Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sandbox

import (
	"context"
	"io"
	"time"
)

// Sandbox 定义容器执行后端，调用顺序为 Create、Start、Execute、Stop 或 Remove
// 接口本身不代表多租户安全保证，也不会自动接入 Agent 执行路径
type Sandbox interface {
	Create(context.Context, *CreateOptions) (string, error)
	Start(context.Context, string) error
	Execute(context.Context, string, []string, *ExecuteOptions) (*ExecuteResult, error)
	Stop(context.Context, string, time.Duration) error
	Remove(context.Context, string) error
	GetLogs(context.Context, string, *LogOptions) (io.ReadCloser, error)
	Cleanup(context.Context) error
	Close() error
}

// CreateOptions 定义容器初始化配置
// Mounts 只能来自可信宿主配置，不能直接接受不可信流水线输入
type CreateOptions struct {
	Image string
	// Command 必須显式指定长期运行的容器 init 命令
	Command    []string
	Args       []string
	Env        map[string]string
	WorkingDir string
	// NetworkMode 仅支持 none，空值使用默认离线模式
	NetworkMode string
	Resources   *Resources
	Mounts      []Mount
	Labels      map[string]string
	Hostname    string
	// Privileged 当前不支持，true 会返回错误
	Privileged bool
}

// Resources 定义 CPU、内存限制和可选权重，非法值会返回错误
// 空字段不施加对应限制，MemoryReservation 仅是软保留而非最低可用内存保证
type Resources struct {
	CPU               string
	Memory            string
	CPUShares         int64
	MemoryReservation string
}

// Mount 定义可信宿主上的绑定挂载，不支持 volume 或 tmpfs
// ReadOnly 不应被视为对子挂载和所有文件系统配置的完整安全保证
type Mount struct {
	Source   string
	Target   string
	Type     string
	ReadOnly bool
}

// ExecuteOptions 定义单次命令环境、目录、数字用户和输出流
// User 仅支持 UID 或 UID:GID，只有 UID 时 GID 也使用该值
// 流由调用方拥有，读写实现不得无限阻塞
type ExecuteOptions struct {
	Env        map[string]string
	WorkingDir string
	User       string
	// TTY 当前不支持，true 会返回错误
	TTY     bool
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
	Timeout time.Duration
}

// ExecuteResult 返回退出状态和各自至多 4 MiB 的标准输出与标准错误
// 非零退出码与传输错误分开，调用方必须同时检查 error 和 ExitCode
type ExecuteResult struct {
	ExitCode        int32
	Stdout          string
	Stderr          string
	OutputTruncated bool
	StartTime       time.Time
	EndTime         time.Time
	Duration        time.Duration
}

// LogOptions 保留日志查询选项，当前后端只支持全部为零的内存快照查询
// 非零选项返回错误，日志不持久化且删除容器后不可查询
type LogOptions struct {
	Follow     bool
	Tail       int
	Since      time.Time
	Until      time.Time
	Timestamps bool
}
