package logstream

// SpoolConfig 配置已脱敏构建日志的本地持久化队列。
// Directory 必须是本进程独占、跨重启保留的私有目录。
type SpoolConfig struct {
	Directory      string `mapstructure:"directory" json:"directory"`
	MaxDiskUsageMB int64  `mapstructure:"maxDiskUsageMB" json:"maxDiskUsageMB"`
}
