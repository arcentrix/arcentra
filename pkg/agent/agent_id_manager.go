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

package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/arcentrix/arcentra/pkg/id"
)

// Info Agent ID信息
type Info struct {
	AgentID       string    `json:"agent_id"`
	RegisteredAt  time.Time `json:"registered_at"`
	ServerAddress string    `json:"server_address"`
	Hostname      string    `json:"hostname"`
	LastUpdateAt  time.Time `json:"last_update_at"`
}

// AgentIDManager Agent ID管理器
type AgentIDManager struct {
	idFilePath string
	info       *Info
}

// NewAgentIDManager 创建Agent ID管理器
func NewAgentIDManager(idFilePath string) *AgentIDManager {
	return &AgentIDManager{
		idFilePath: idFilePath,
	}
}

// LoadOrGenerate 加载或生成Agent ID
// 返回值：agentID, isNew, error
func (m *AgentIDManager) LoadOrGenerate(hostname, serverAddr string) (string, bool, error) {
	// 1. 尝试从文件加载
	if info, err := m.loadFromFile(); err == nil && info.AgentID != "" {
		m.info = info
		// 更新最后使用时间
		m.info.LastUpdateAt = time.Now()
		m.info.Hostname = hostname
		m.info.ServerAddress = serverAddr
		_ = m.saveToFile() // 忽略保存错误
		return info.AgentID, false, nil
	}

	// 2. 生成新的Agent ID（使用ULID）
	agentID := id.GetUild()
	m.info = &Info{
		AgentID:       agentID,
		RegisteredAt:  time.Now(),
		ServerAddress: serverAddr,
		Hostname:      hostname,
		LastUpdateAt:  time.Now(),
	}

	return agentID, true, nil
}

// UpdateAfterRegister 注册成功后更新Agent ID信息
// Server可能会返回不同的ID（如果发生ID冲突或重新分配）
func (m *AgentIDManager) UpdateAfterRegister(agentID, serverAddr string) error {
	if m.info == nil {
		m.info = &Info{}
	}

	// 如果Server返回的ID与本地不同，使用Server的ID
	if agentID != m.info.AgentID {
		m.info.AgentID = agentID
		m.info.RegisteredAt = time.Now()
	}

	m.info.ServerAddress = serverAddr
	m.info.LastUpdateAt = time.Now()

	// 持久化保存
	return m.saveToFile()
}

// GetAgentID 获取当前的Agent ID
func (m *AgentIDManager) GetAgentID() string {
	if m.info == nil {
		return ""
	}
	return m.info.AgentID
}

// GetInfo 获取Agent ID完整信息
func (m *AgentIDManager) GetInfo() *Info {
	return m.info
}

// loadFromFile 从文件加载Agent ID信息
func (m *AgentIDManager) loadFromFile() (*Info, error) {
	data, err := os.ReadFile(m.idFilePath)
	if err != nil {
		return nil, fmt.Errorf("读取agent id文件失败: %w", err)
	}

	var info Info
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("解析agent id文件失败: %w", err)
	}

	return &info, nil
}

// saveToFile 保存Agent ID信息到文件
func (m *AgentIDManager) saveToFile() error {
	if m.info == nil {
		return fmt.Errorf("agent id信息为空")
	}

	// 确保目录存在
	dir := filepath.Dir(m.idFilePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}

	// 序列化为JSON
	data, err := json.MarshalIndent(m.info, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化agent id信息失败: %w", err)
	}

	// 写入文件（原子写入：先写临时文件再重命名）
	tempFile := m.idFilePath + ".tmp"
	if err := os.WriteFile(tempFile, data, 0o600); err != nil {
		return fmt.Errorf("写入agent id文件失败: %w", err)
	}

	if err := os.Rename(tempFile, m.idFilePath); err != nil {
		_ = os.Remove(tempFile)
		return fmt.Errorf("重命名agent id文件失败: %w", err)
	}

	return nil
}

// Reset 重置Agent ID（用于测试或重新注册）
func (m *AgentIDManager) Reset() error {
	if err := os.Remove(m.idFilePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除agent id文件失败: %w", err)
	}
	m.info = nil
	return nil
}

// GetDefaultIDFilePath 获取默认的ID文件路径
func GetDefaultIDFilePath() string {
	// 根据操作系统返回不同的路径
	switch {
	case fileExists("/var/lib"):
		return "/var/lib/arcentra/agent/agent.id"
	case fileExists("/Library"):
		return "/Library/Application Support/arcentra/agent.id"
	default:
		// Windows 或其他系统，使用当前目录
		return "./data/agent.id"
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
