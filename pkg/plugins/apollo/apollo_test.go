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

package apollo

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pluginpkg "github.com/arcentrix/arcentra/pkg/plugin"
	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewApollo(t *testing.T) {
	p := NewApollo()
	assert.NotNil(t, p)
	assert.Equal(t, "apollo", p.Name())
	assert.Equal(t, "1.0.0", p.Version())
	assert.Equal(t, pluginpkg.TypeIntegration, p.Type())
	assert.Equal(t, 30, p.cfg.Timeout)
}

func TestApollo_Init(t *testing.T) {
	tests := []struct {
		name   string
		config json.RawMessage
		check  func(t *testing.T, p *Apollo)
	}{
		{
			name:   "empty config",
			config: json.RawMessage{},
			check: func(t *testing.T, p *Apollo) {
				assert.Equal(t, 30, p.cfg.Timeout)
			},
		},
		{
			name:   "custom config",
			config: json.RawMessage(`{"portalUrl":"http://portal:8070","token":"tk","timeout":60}`),
			check: func(t *testing.T, p *Apollo) {
				assert.Equal(t, "http://portal:8070", p.cfg.PortalURL)
				assert.Equal(t, "tk", p.cfg.Token)
				assert.Equal(t, 60, p.cfg.Timeout)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewApollo()
			err := p.Init(tt.config)
			require.NoError(t, err)
			tt.check(t, p)
		})
	}
}

func TestApollo_Cleanup(t *testing.T) {
	assert.NoError(t, NewApollo().Cleanup())
}

func TestApollo_UnknownAction(t *testing.T) {
	p := NewApollo()
	_ = p.Init(json.RawMessage{})
	_, err := p.Execute("nope", nil, nil)
	assert.ErrorContains(t, err, "unknown action")
}

// ---------------------------------------------------------------------------
// config.get
// ---------------------------------------------------------------------------

func TestConfigGet_MissingFields(t *testing.T) {
	p := newTestApollo()
	for _, tc := range []struct {
		name   string
		args   ConfigGetArgs
		errMsg string
	}{
		{"no portal_url", ConfigGetArgs{AppID: "a", Env: "DEV", Key: "k"}, "portal_url is required"},
		{"no app_id", ConfigGetArgs{PortalURL: "http://x", Env: "DEV", Key: "k"}, "app_id is required"},
		{"no env", ConfigGetArgs{PortalURL: "http://x", AppID: "a", Key: "k"}, "env is required"},
		{"no key", ConfigGetArgs{PortalURL: "http://x", AppID: "a", Env: "DEV"}, "key is required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params, _ := sonic.Marshal(tc.args)
			_, err := p.Execute("config.get", params, nil)
			assert.ErrorContains(t, err, tc.errMsg)
		})
	}
}

func TestConfigGet_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "my-token", r.Header.Get("Authorization"))
		assert.True(t, strings.Contains(r.URL.Path, "/items/db.url"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"key":"db.url","value":"jdbc:mysql://localhost:3306"}`))
	}))
	defer srv.Close()

	p := newTestApollo()
	params, _ := sonic.Marshal(ConfigGetArgs{
		PortalURL: srv.URL,
		Token:     "my-token",
		AppID:     "app1",
		Env:       "DEV",
		Key:       "db.url",
	})
	result, err := p.Execute("config.get", params, nil)
	require.NoError(t, err)

	var m map[string]any
	require.NoError(t, sonic.Unmarshal(result, &m))
	assert.True(t, m["success"].(bool))
	data := m["data"].(map[string]any)
	assert.Equal(t, "db.url", data["key"])
}

func TestConfigGet_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	p := newTestApollo()
	params, _ := sonic.Marshal(ConfigGetArgs{PortalURL: srv.URL, Token: "t", AppID: "a", Env: "DEV", Key: "k"})
	_, err := p.Execute("config.get", params, nil)
	assert.ErrorContains(t, err, "configuration not found")
}

// ---------------------------------------------------------------------------
// config.update
// ---------------------------------------------------------------------------

func TestConfigUpdate_CreateNew(t *testing.T) {
	var createdBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method == http.MethodPost {
			_ = json.NewDecoder(r.Body).Decode(&createdBody)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"key":"k1","value":"v1"}`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	p := newTestApollo()
	params, _ := sonic.Marshal(ConfigUpdateArgs{
		PortalURL: srv.URL, Token: "t", AppID: "a", Env: "DEV",
		Key: "k1", Value: "v1", Operator: "ci",
	})
	result, err := p.Execute("config.update", params, nil)
	require.NoError(t, err)

	var m map[string]any
	require.NoError(t, sonic.Unmarshal(result, &m))
	assert.True(t, m["success"].(bool))
	assert.Equal(t, "created", m["action"])
	assert.Equal(t, "k1", createdBody["key"])
	assert.Equal(t, "v1", createdBody["value"])
}

func TestConfigUpdate_UpdateExisting(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"key":"k1","value":"old"}`))
			return
		}
		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	p := newTestApollo()
	params, _ := sonic.Marshal(ConfigUpdateArgs{
		PortalURL: srv.URL, Token: "t", AppID: "a", Env: "DEV",
		Key: "k1", Value: "new-val", Operator: "ci",
	})
	result, err := p.Execute("config.update", params, nil)
	require.NoError(t, err)

	var m map[string]any
	require.NoError(t, sonic.Unmarshal(result, &m))
	assert.Equal(t, "updated", m["action"])
}

func TestConfigUpdate_ValueFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		assert.Equal(t, "file-content", body["value"])
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "val.txt"), []byte("file-content"), 0o644))

	p := newTestApollo()
	params, _ := sonic.Marshal(ConfigUpdateArgs{
		PortalURL: srv.URL, Token: "t", AppID: "a", Env: "DEV",
		Key: "cert", ValueFile: "val.txt", Operator: "ci",
	})
	opts, _ := sonic.Marshal(map[string]any{"workspace": workspace})
	_, err := p.Execute("config.update", params, opts)
	require.NoError(t, err)
}

func TestConfigUpdate_NoValue(t *testing.T) {
	p := newTestApollo()
	params, _ := sonic.Marshal(ConfigUpdateArgs{
		PortalURL: "http://x", Token: "t", AppID: "a", Env: "DEV",
		Key: "k", Operator: "ci",
	})
	_, err := p.Execute("config.update", params, nil)
	assert.ErrorContains(t, err, "either value or value_file is required")
}

// ---------------------------------------------------------------------------
// config.delete
// ---------------------------------------------------------------------------

func TestConfigDelete_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		assert.Equal(t, "ci", r.URL.Query().Get("operator"))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := newTestApollo()
	params, _ := sonic.Marshal(ConfigDeleteArgs{
		PortalURL: srv.URL, Token: "t", AppID: "a", Env: "DEV",
		Key: "k1", Operator: "ci",
	})
	result, err := p.Execute("config.delete", params, nil)
	require.NoError(t, err)

	var m map[string]any
	require.NoError(t, sonic.Unmarshal(result, &m))
	assert.True(t, m["success"].(bool))
}

// ---------------------------------------------------------------------------
// config.release
// ---------------------------------------------------------------------------

func TestConfigRelease_Success(t *testing.T) {
	var releaseBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.True(t, strings.Contains(r.URL.Path, "/releases"))
		_ = json.NewDecoder(r.Body).Decode(&releaseBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"appId":"a","clusterName":"default"}`))
	}))
	defer srv.Close()

	p := newTestApollo()
	params, _ := sonic.Marshal(ConfigReleaseArgs{
		PortalURL: srv.URL, Token: "t", AppID: "a", Env: "DEV",
		ReleaseTitle: "v1.0", ReleasedBy: "ci",
	})
	result, err := p.Execute("config.release", params, nil)
	require.NoError(t, err)

	var m map[string]any
	require.NoError(t, sonic.Unmarshal(result, &m))
	assert.True(t, m["success"].(bool))
	assert.Equal(t, "v1.0", releaseBody["releaseTitle"])
	assert.Equal(t, "ci", releaseBody["releasedBy"])
}

func TestConfigRelease_MissingFields(t *testing.T) {
	p := newTestApollo()
	params, _ := sonic.Marshal(ConfigReleaseArgs{PortalURL: "http://x", Token: "t", AppID: "a"})
	_, err := p.Execute("config.release", params, nil)
	assert.Error(t, err)
}

// ---------------------------------------------------------------------------
// namespace.import
// ---------------------------------------------------------------------------

func TestNamespaceImport_Properties(t *testing.T) {
	calls := make(map[string]string)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method == http.MethodPost {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			calls[body["key"].(string)] = "created"
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	workspace := t.TempDir()
	content := "# comment\ndb.url=jdbc:mysql://localhost\ndb.user = admin\n\ndb.pool=10\n"
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "app.properties"), []byte(content), 0o644))

	p := newTestApollo()
	params, _ := sonic.Marshal(NamespaceImportArgs{
		PortalURL: srv.URL, Token: "t", AppID: "a", Env: "DEV",
		File: "app.properties", Format: "properties", Operator: "ci",
	})
	opts, _ := sonic.Marshal(map[string]any{"workspace": workspace})
	result, err := p.Execute("namespace.import", params, opts)
	require.NoError(t, err)

	var m map[string]any
	require.NoError(t, sonic.Unmarshal(result, &m))
	assert.True(t, m["success"].(bool))
	assert.Equal(t, float64(3), m["imported"])
	assert.Contains(t, calls, "db.url")
	assert.Contains(t, calls, "db.user")
	assert.Contains(t, calls, "db.pool")
}

func TestNamespaceImport_YAML(t *testing.T) {
	calls := make(map[string]string)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		calls[body["key"].(string)] = body["value"].(string)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	workspace := t.TempDir()
	yamlContent := "server:\n  port: 8080\n  host: localhost\n"
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "app.yaml"), []byte(yamlContent), 0o644))

	p := newTestApollo()
	params, _ := sonic.Marshal(NamespaceImportArgs{
		PortalURL: srv.URL, Token: "t", AppID: "a", Env: "DEV",
		File: "app.yaml", Operator: "ci",
	})
	opts, _ := sonic.Marshal(map[string]any{"workspace": workspace})
	result, err := p.Execute("namespace.import", params, opts)
	require.NoError(t, err)

	var m map[string]any
	require.NoError(t, sonic.Unmarshal(result, &m))
	assert.True(t, m["success"].(bool))
	assert.Equal(t, "8080", calls["server.port"])
	assert.Equal(t, "localhost", calls["server.host"])
}

func TestNamespaceImport_JSON(t *testing.T) {
	calls := make(map[string]string)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		calls[body["key"].(string)] = body["value"].(string)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	workspace := t.TempDir()
	jsonContent := `{"cache":{"ttl":300,"enabled":true}}`
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "cfg.json"), []byte(jsonContent), 0o644))

	p := newTestApollo()
	params, _ := sonic.Marshal(NamespaceImportArgs{
		PortalURL: srv.URL, Token: "t", AppID: "a", Env: "DEV",
		File: "cfg.json", Format: "json", Operator: "ci",
	})
	opts, _ := sonic.Marshal(map[string]any{"workspace": workspace})
	result, err := p.Execute("namespace.import", params, opts)
	require.NoError(t, err)

	var m map[string]any
	require.NoError(t, sonic.Unmarshal(result, &m))
	assert.True(t, m["success"].(bool))
	assert.Equal(t, "300", calls["cache.ttl"])
	assert.Equal(t, "true", calls["cache.enabled"])
}

func TestNamespaceImport_MissingFile(t *testing.T) {
	p := newTestApollo()
	params, _ := sonic.Marshal(NamespaceImportArgs{
		PortalURL: "http://x", Token: "t", AppID: "a", Env: "DEV",
		Operator: "ci",
	})
	_, err := p.Execute("namespace.import", params, nil)
	assert.ErrorContains(t, err, "file is required")
}

func TestNamespaceImport_UnsupportedFormat(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "f.xml"), []byte("<x/>"), 0o644))

	p := newTestApollo()
	params, _ := sonic.Marshal(NamespaceImportArgs{
		PortalURL: "http://x", Token: "t", AppID: "a", Env: "DEV",
		File: "f.xml", Format: "xml", Operator: "ci",
	})
	opts, _ := sonic.Marshal(map[string]any{"workspace": workspace})
	_, err := p.Execute("namespace.import", params, opts)
	assert.ErrorContains(t, err, "unsupported format")
}

// ---------------------------------------------------------------------------
// Config file parsing
// ---------------------------------------------------------------------------

func TestParseProperties(t *testing.T) {
	input := "# comment\nkey1=val1\nkey2 : val2\n\n!ignored\nkey3=\n"
	m, err := parseProperties([]byte(input))
	require.NoError(t, err)
	assert.Equal(t, "val1", m["key1"])
	assert.Equal(t, "val2", m["key2"])
	assert.Equal(t, "", m["key3"])
	assert.NotContains(t, m, "# comment")
}

func TestParseYAML(t *testing.T) {
	input := "a:\n  b: 1\n  c: hello\n"
	m, err := parseYAML([]byte(input))
	require.NoError(t, err)
	assert.Equal(t, "1", m["a.b"])
	assert.Equal(t, "hello", m["a.c"])
}

func TestParseJSON(t *testing.T) {
	input := `{"x":{"y":42},"z":"ok"}`
	m, err := parseJSON([]byte(input))
	require.NoError(t, err)
	assert.Equal(t, "42", m["x.y"])
	assert.Equal(t, "ok", m["z"])
}

func TestInferFormat(t *testing.T) {
	assert.Equal(t, "properties", inferFormat("app.properties"))
	assert.Equal(t, "yaml", inferFormat("config.yaml"))
	assert.Equal(t, "yaml", inferFormat("config.yml"))
	assert.Equal(t, "json", inferFormat("data.json"))
	assert.Equal(t, "properties", inferFormat("unknown.txt"))
}

// ---------------------------------------------------------------------------
// File reading
// ---------------------------------------------------------------------------

func TestReadWorkspaceFile_PathTraversal(t *testing.T) {
	ws := t.TempDir()
	_, err := readWorkspaceFile(ws, "../../etc/passwd")
	assert.ErrorContains(t, err, "escapes workspace boundary")
}

func TestReadWorkspaceFile_OK(t *testing.T) {
	ws := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(ws, "f.txt"), []byte("data"), 0o644))
	d, err := readWorkspaceFile(ws, "f.txt")
	require.NoError(t, err)
	assert.Equal(t, "data", string(d))
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newTestApollo() *Apollo {
	p := NewApollo()
	_ = p.Init(json.RawMessage{})
	return p
}
