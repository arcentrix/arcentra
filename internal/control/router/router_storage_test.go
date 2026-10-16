// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/require"
)

func storageJSONBody(t *testing.T, values map[string]any) string {
	t.Helper()
	raw, err := sonic.Marshal(values)
	require.NoError(t, err)
	return string(raw)
}

func createStorageHTTPFixture(t *testing.T, f adminFixture) {
	t.Helper()
	config := base64.StdEncoding.EncodeToString([]byte(`{"endpoint":"127.0.0.1:9000","bucket":"test","region":"","basePath":"","useTLS":false,"accessKey":"access-canary","secretKey":"storage-canary"}`))
	status, _, raw := f.request(t, "admin", "POST", "/storage/configs", storageJSONBody(t, map[string]any{"storageId": "test", "name": "Original", "description": "Keep me", "storageType": "minio", "config": config, "isDefault": 1}))
	require.Equal(t, 200, status, raw)
	require.NotContains(t, raw, "canary")
}

func TestStorageEditorOmittedFieldsPreservedAndExplicitEmptyDescriptionPersisted(t *testing.T) {
	f := newAdminFixture(t)
	createStorageHTTPFixture(t, f)
	for _, config := range []any{nil, "", base64.StdEncoding.EncodeToString([]byte(`{}`)), base64.StdEncoding.EncodeToString([]byte(`{"accessKey":"","secretKey":null}`))} {
		body := map[string]any{"name": "Renamed", "config": config}
		status, result, raw := f.request(t, "admin", "PUT", "/storage/configs/test", storageJSONBody(t, body))
		require.Equal(t, 200, status, raw)
		require.NotContains(t, raw, "canary")
		view := result["detail"].(map[string]any)
		require.Equal(t, "Keep me", view["description"])
		require.Equal(t, float64(1), view["isDefault"])
		require.Equal(t, float64(1), view["isEnabled"])
		stored, err := f.rt.Services.Storage.GetStorageConfig(context.Background(), "test")
		require.NoError(t, err)
		require.Contains(t, string(stored.Config), "storage-canary")
		require.Contains(t, string(stored.Config), "access-canary")
	}
	status, _, raw := f.request(t, "admin", "PUT", "/storage/configs/test", `{"description":""}`)
	require.Equal(t, 200, status, raw)
	status, result, raw := f.request(t, "admin", "GET", "/storage/configs/test", "")
	require.Equal(t, 200, status, raw)
	require.Equal(t, "", result["detail"].(map[string]any)["description"])
	// Zero flags are explicit; omission above must never accidentally disable a row.
	status, _, raw = f.request(t, "admin", "PUT", "/storage/configs/test", `{"isDefault":0,"isEnabled":0}`)
	require.Equal(t, 200, status, raw)
	var disabled model.StorageConfig
	require.NoError(t, f.db.WithContext(context.Background()).Select("is_default", "is_enabled").Where("storage_id = ?", "test").Take(&disabled).Error)
	require.Zero(t, disabled.IsDefault)
	require.Zero(t, disabled.IsEnabled)
}

func TestStorageEditorRejectsTargetMutationAndMaskedOrMalformedSecrets(t *testing.T) {
	f := newAdminFixture(t)
	createStorageHTTPFixture(t, f)
	for _, patch := range []string{
		`{"endpoint":"other:9000"}`, `{"bucket":"other"}`, `{"region":"other"}`, `{"basePath":"other"}`, `{"useTLS":true}`,
		`{"accessKey":"configured"}`, `{"secretKey":"********"}`, `{"secretKey":false}`, `null`, `[]`,
	} {
		status, _, raw := f.request(t, "admin", "PUT", "/storage/configs/test", storageJSONBody(t, map[string]any{"config": base64.StdEncoding.EncodeToString([]byte(patch))}))
		require.Equal(t, 400, status, patch+" "+raw)
		require.NotContains(t, raw, "canary")
	}
	for _, body := range []string{`{"storageType":"s3"}`, `{"storageId":"other"}`, `{"name":""}`, `{"isDefault":2}`, `{"config":{"secretKey":"must-be-base64"}}`} {
		status, _, _ := f.request(t, "admin", "PUT", "/storage/configs/test", body)
		require.Equal(t, 400, status, body)
	}
	patch := base64.StdEncoding.EncodeToString([]byte(`{"accessKey":"replacement-access-canary","secretKey":"replacement-secret-canary"}`))
	status, _, raw := f.request(t, "admin", "PUT", "/storage/configs/test", storageJSONBody(t, map[string]any{"config": patch}))
	require.Equal(t, 200, status, raw)
	require.NotContains(t, raw, "canary")
	stored, err := f.rt.Services.Storage.GetStorageConfig(context.Background(), "test")
	require.NoError(t, err)
	require.Contains(t, string(stored.Config), "replacement-secret-canary")
	require.Contains(t, string(stored.Config), `"bucket":"test"`)
}

func TestStorageDefaultUsesLiveAuthorityAndRejectsInvalidTarget(t *testing.T) {
	f := newAdminFixture(t)
	createStorageHTTPFixture(t, f)
	status, _, _ := f.request(t, "admin", "POST", "/storage/configs/missing/default", "")
	require.Equal(t, 404, status)
	current, err := f.rt.Services.Storage.GetDefaultStorageConfig(context.Background())
	require.NoError(t, err)
	require.Equal(t, "test", current.StorageID)
	// A previously valid session must not bypass a live platform grant revocation.
	require.NoError(t, f.db.WithContext(context.Background()).Model(&model.RoleGrant{}).Where("subject_id = ?", "admin").Update("is_enabled", 0).Error)
	status, _, _ = f.request(t, "admin", "POST", "/storage/configs/test/default", "")
	require.Equal(t, 403, status)
}

func TestStorageHTTPRejectsInvalidIdentityAndKeepsLegacyReadOnly(t *testing.T) {
	f := newAdminFixture(t)
	config := base64.StdEncoding.EncodeToString([]byte(`{"endpoint":"127.0.0.1:9000","bucket":"test","accessKey":"access-canary","secretKey":"storage-canary"}`))
	for _, id := range []string{strings.Repeat("x", 65), "..", "invalid/identity", "bad:identity"} {
		body := storageJSONBody(t, map[string]any{"storageId": id, "name": "Test", "storageType": "minio", "config": config})
		status, _, raw := f.request(t, "admin", "POST", "/storage/configs", body)
		require.Equal(t, 400, status, raw)
	}
	legacyID := strings.Repeat("l", 65)
	require.NoError(t, f.db.WithContext(context.Background()).Create(&model.StorageConfig{StorageID: legacyID, Name: "Legacy", StorageType: "minio", Config: []byte(`{}`), IsEnabled: 1}).Error)
	status, result, raw := f.request(t, "admin", "GET", "/storage/configs", "")
	require.Equal(t, 200, status, raw)
	require.Len(t, result["detail"].([]any), 1)
	status, _, _ = f.request(t, "admin", "POST", "/storage/configs/"+legacyID+"/default", "")
	require.Equal(t, 400, status)
	status, _, _ = f.request(t, "admin", "PUT", "/storage/configs/"+legacyID, `{"name":"Edited"}`)
	require.Equal(t, 400, status)
}
