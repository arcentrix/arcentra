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

package nacos

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	pluginpkg "github.com/arcentrix/arcentra/pkg/plugin"
	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewNacos(t *testing.T) {
	p := NewNacos()
	assert.NotNil(t, p)
	assert.Equal(t, "nacos", p.Name())
	assert.Equal(t, "1.0.0", p.Version())
	assert.Equal(t, pluginpkg.TypeIntegration, p.Type())
	assert.Equal(t, 30, p.cfg.Timeout)
}

func TestNacos_Init(t *testing.T) {
	tests := []struct {
		name   string
		config json.RawMessage
		check  func(t *testing.T, p *Nacos)
	}{
		{
			name:   "empty config",
			config: json.RawMessage{},
			check: func(t *testing.T, p *Nacos) {
				assert.Equal(t, 30, p.cfg.Timeout)
			},
		},
		{
			name:   "custom config",
			config: json.RawMessage(`{"serverAddr":"http://nacos:8848","username":"admin","timeout":60}`),
			check: func(t *testing.T, p *Nacos) {
				assert.Equal(t, "http://nacos:8848", p.cfg.ServerAddr)
				assert.Equal(t, "admin", p.cfg.Username)
				assert.Equal(t, 60, p.cfg.Timeout)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewNacos()
			err := p.Init(tt.config)
			require.NoError(t, err)
			tt.check(t, p)
		})
	}
}

func TestNacos_Cleanup(t *testing.T) {
	p := NewNacos()
	assert.NoError(t, p.Cleanup())
}

func TestNacos_UnknownAction(t *testing.T) {
	p := NewNacos()
	_ = p.Init(json.RawMessage{})
	_, err := p.Execute("no_such_action", nil, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown action")
}

// ---------------------------------------------------------------------------
// config.get
// ---------------------------------------------------------------------------

func TestConfigGet_MissingServerAddr(t *testing.T) {
	p := NewNacos()
	_ = p.Init(json.RawMessage{})
	params, _ := sonic.Marshal(ConfigGetArgs{DataID: "app.yaml", Group: "DEFAULT_GROUP"})
	_, err := p.Execute("config.get", params, nil)
	assert.ErrorContains(t, err, "server_addr is required")
}

func TestConfigGet_MissingDataID(t *testing.T) {
	p := NewNacos()
	_ = p.Init(json.RawMessage(`{"serverAddr":"http://localhost:8848"}`))
	params, _ := sonic.Marshal(ConfigGetArgs{Group: "DEFAULT_GROUP"})
	_, err := p.Execute("config.get", params, nil)
	assert.ErrorContains(t, err, "data_id is required")
}

func TestConfigGet_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/nacos/v1/cs/configs" && r.Method == http.MethodGet {
			assert.Equal(t, "app.yaml", r.URL.Query().Get("dataId"))
			assert.Equal(t, "DEFAULT_GROUP", r.URL.Query().Get("group"))
			assert.Equal(t, "ns1", r.URL.Query().Get("tenant"))
			_, _ = w.Write([]byte("server:\n  port: 8080\n"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	p := NewNacos()
	_ = p.Init(json.RawMessage{})
	params, _ := sonic.Marshal(ConfigGetArgs{
		ServerAddr: srv.URL,
		DataID:     "app.yaml",
		Group:      "DEFAULT_GROUP",
		Namespace:  "ns1",
	})
	result, err := p.Execute("config.get", params, nil)
	require.NoError(t, err)

	var m map[string]any
	require.NoError(t, sonic.Unmarshal(result, &m))
	assert.True(t, m["success"].(bool))
	assert.Contains(t, m["content"], "port: 8080")
}

func TestConfigGet_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	p := NewNacos()
	_ = p.Init(json.RawMessage{})
	params, _ := sonic.Marshal(ConfigGetArgs{ServerAddr: srv.URL, DataID: "nope", Group: "G"})
	_, err := p.Execute("config.get", params, nil)
	assert.ErrorContains(t, err, "configuration not found")
}

// ---------------------------------------------------------------------------
// config.publish
// ---------------------------------------------------------------------------

func TestConfigPublish_InlineContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/nacos/v1/cs/configs" && r.Method == http.MethodPost {
			_ = r.ParseForm()
			assert.Equal(t, "app.yaml", r.FormValue("dataId"))
			assert.Equal(t, "yaml", r.FormValue("type"))
			assert.Contains(t, r.FormValue("content"), "port: 8080")
			_, _ = w.Write([]byte("true"))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	p := NewNacos()
	_ = p.Init(json.RawMessage{})
	params, _ := sonic.Marshal(ConfigPublishArgs{
		ServerAddr: srv.URL,
		DataID:     "app.yaml",
		Group:      "DEFAULT_GROUP",
		Content:    "server:\n  port: 8080\n",
		Type:       "yaml",
	})
	result, err := p.Execute("config.publish", params, nil)
	require.NoError(t, err)

	var m map[string]any
	require.NoError(t, sonic.Unmarshal(result, &m))
	assert.True(t, m["success"].(bool))
}

func TestConfigPublish_ContentFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/nacos/v1/cs/configs" && r.Method == http.MethodPost {
			_ = r.ParseForm()
			assert.Contains(t, r.FormValue("content"), "from-file")
			_, _ = w.Write([]byte("true"))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	workspace := t.TempDir()
	cfgDir := filepath.Join(workspace, "config")
	require.NoError(t, os.MkdirAll(cfgDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "app.yaml"), []byte("key: from-file\n"), 0o644))

	p := NewNacos()
	_ = p.Init(json.RawMessage{})
	params, _ := sonic.Marshal(ConfigPublishArgs{
		ServerAddr:  srv.URL,
		DataID:      "app.yaml",
		Group:       "DEFAULT_GROUP",
		ContentFile: "config/app.yaml",
		Type:        "yaml",
	})
	opts, _ := sonic.Marshal(map[string]any{"workspace": workspace})
	result, err := p.Execute("config.publish", params, opts)
	require.NoError(t, err)

	var m map[string]any
	require.NoError(t, sonic.Unmarshal(result, &m))
	assert.True(t, m["success"].(bool))
}

func TestConfigPublish_ContentFilePrecedence(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		assert.Contains(t, r.FormValue("content"), "file-wins")
		_, _ = w.Write([]byte("true"))
	}))
	defer srv.Close()

	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "f.txt"), []byte("file-wins"), 0o644))

	p := NewNacos()
	_ = p.Init(json.RawMessage{})
	params, _ := sonic.Marshal(ConfigPublishArgs{
		ServerAddr:  srv.URL,
		DataID:      "d",
		Group:       "G",
		Content:     "inline-content",
		ContentFile: "f.txt",
	})
	opts, _ := sonic.Marshal(map[string]any{"workspace": workspace})
	_, err := p.Execute("config.publish", params, opts)
	require.NoError(t, err)
}

func TestConfigPublish_NoContent(t *testing.T) {
	p := NewNacos()
	_ = p.Init(json.RawMessage(`{"serverAddr":"http://localhost:8848"}`))
	params, _ := sonic.Marshal(ConfigPublishArgs{DataID: "d", Group: "G"})
	_, err := p.Execute("config.publish", params, nil)
	assert.ErrorContains(t, err, "either content or content_file is required")
}

func TestConfigPublish_MissingServerAddr(t *testing.T) {
	p := NewNacos()
	_ = p.Init(json.RawMessage{})
	params, _ := sonic.Marshal(ConfigPublishArgs{DataID: "d", Content: "c"})
	_, err := p.Execute("config.publish", params, nil)
	assert.ErrorContains(t, err, "server_addr is required")
}

func TestConfigPublish_MissingDataID(t *testing.T) {
	p := NewNacos()
	_ = p.Init(json.RawMessage(`{"serverAddr":"http://localhost:8848"}`))
	params, _ := sonic.Marshal(ConfigPublishArgs{Content: "c"})
	_, err := p.Execute("config.publish", params, nil)
	assert.ErrorContains(t, err, "data_id is required")
}

// ---------------------------------------------------------------------------
// config.delete
// ---------------------------------------------------------------------------

func TestConfigDelete_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			assert.Equal(t, "d1", r.URL.Query().Get("dataId"))
			_, _ = w.Write([]byte("true"))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	p := NewNacos()
	_ = p.Init(json.RawMessage{})
	params, _ := sonic.Marshal(ConfigDeleteArgs{ServerAddr: srv.URL, DataID: "d1", Group: "G"})
	result, err := p.Execute("config.delete", params, nil)
	require.NoError(t, err)

	var m map[string]any
	require.NoError(t, sonic.Unmarshal(result, &m))
	assert.True(t, m["success"].(bool))
}

// ---------------------------------------------------------------------------
// Auth token
// ---------------------------------------------------------------------------

func TestEnsureToken_WithCredentials(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/nacos/v1/auth/login" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"accessToken":"tk123","tokenTtl":600}`))
			return
		}
		if r.URL.Path == "/nacos/v1/cs/configs" {
			assert.Equal(t, "tk123", r.URL.Query().Get("accessToken"))
			_, _ = w.Write([]byte("content"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	p := NewNacos()
	_ = p.Init(json.RawMessage{})
	params, _ := sonic.Marshal(ConfigGetArgs{
		ServerAddr: srv.URL,
		Username:   "u",
		Password:   "p",
		DataID:     "d",
		Group:      "G",
	})
	result, err := p.Execute("config.get", params, nil)
	require.NoError(t, err)

	var m map[string]any
	require.NoError(t, sonic.Unmarshal(result, &m))
	assert.True(t, m["success"].(bool))
}

// ---------------------------------------------------------------------------
// File reading utilities
// ---------------------------------------------------------------------------

func TestReadWorkspaceFile_PathTraversal(t *testing.T) {
	workspace := t.TempDir()
	_, err := readWorkspaceFile(workspace, "../../etc/passwd")
	assert.ErrorContains(t, err, "escapes workspace boundary")
}

func TestReadWorkspaceFile_EmptyPath(t *testing.T) {
	_, err := readWorkspaceFile("/tmp", "")
	assert.ErrorContains(t, err, "file path is empty")
}

func TestReadWorkspaceFile_NoWorkspace(t *testing.T) {
	_, err := readWorkspaceFile("", "file.txt")
	assert.ErrorContains(t, err, "workspace is not available")
}

func TestReadWorkspaceFile_FileNotExist(t *testing.T) {
	workspace := t.TempDir()
	_, err := readWorkspaceFile(workspace, "no-such-file.txt")
	assert.Error(t, err)
	assert.ErrorContains(t, err, "failed to read file")
}

func TestReadWorkspaceFile_OK(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "ok.txt"), []byte("hello"), 0o644))
	data, err := readWorkspaceFile(workspace, "ok.txt")
	require.NoError(t, err)
	assert.Equal(t, "hello", string(data))
}

func TestParseWorkspace(t *testing.T) {
	tests := []struct {
		name string
		opts json.RawMessage
		want string
	}{
		{"nil", nil, ""},
		{"empty", json.RawMessage(`{}`), ""},
		{"present", json.RawMessage(`{"workspace":"/ws"}`), "/ws"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, parseWorkspace(tt.opts))
		})
	}
}

func TestRedact(t *testing.T) {
	assert.Equal(t, "error: ***", redact("error: secret123", "secret123"))
	assert.Equal(t, "safe", redact("safe", ""))
	assert.Equal(t, "", redact("", "x"))
}
