package executor

// CloseLogPublisher 停止本管理器独占的日志发布器，保留持久化待发日志。
// 共享发布器应由创建它的进程负责关闭，而不是逐步骤关闭。
func (m *Manager) CloseLogPublisher() {
	if publisher, ok := m.getLogPublisher().(interface{ Close() }); ok {
		publisher.Close()
	}
}
