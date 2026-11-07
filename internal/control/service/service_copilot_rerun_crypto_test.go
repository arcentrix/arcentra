// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"bytes"
	"strings"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/stretchr/testify/require"
)

func TestCopilotRerunCipherBinding(t *testing.T) {
	key := bytes.Repeat([]byte{0x21}, 32)
	c, err := newCopilotRerunCipher(key)
	require.NoError(t, err)
	b := copilotRerunCipherBinding{Version: 1, Role: "definition", ProjectID: "project", PipelineID: "pipeline", RunID: "run", ObjectID: "snapshot", ContentBinding: "v1:" + strings.Repeat("a", 64)}
	plain := []byte("synthetic private definition")
	sealed, err := c.seal(b, plain)
	require.NoError(t, err)
	second, err := c.seal(b, plain)
	require.NoError(t, err)
	require.NotEqual(t, sealed, second)
	require.False(t, bytes.Contains(sealed, plain))
	restarted, err := newCopilotRerunCipher(key)
	require.NoError(t, err)
	opened, err := restarted.open(b, sealed)
	require.NoError(t, err)
	require.Equal(t, plain, opened)
	mutations := map[string]func(*copilotRerunCipherBinding){
		"version":  func(v *copilotRerunCipherBinding) { v.Version++ },
		"role":     func(v *copilotRerunCipherBinding) { v.Role = "dispatch" },
		"project":  func(v *copilotRerunCipherBinding) { v.ProjectID += "2" },
		"pipeline": func(v *copilotRerunCipherBinding) { v.PipelineID += "2" },
		"run":      func(v *copilotRerunCipherBinding) { v.RunID += "2" },
		"object":   func(v *copilotRerunCipherBinding) { v.ObjectID += "2" },
		"digest":   func(v *copilotRerunCipherBinding) { v.ContentBinding = "v1:" + strings.Repeat("b", 64) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := b
			mutate(&changed)
			out, err := c.open(changed, sealed)
			require.ErrorIs(t, err, errCopilotRerunCipher)
			require.Nil(t, out)
		})
	}
	for _, offset := range []int{0, len(sealed) / 2, len(sealed) - 1} {
		bad := bytes.Clone(sealed)
		bad[offset] ^= 1
		out, err := c.open(b, bad)
		require.ErrorIs(t, err, errCopilotRerunCipher)
		require.Nil(t, out)
	}
	wrong, err := newCopilotRerunCipher(bytes.Repeat([]byte{0x22}, 32))
	require.NoError(t, err)
	_, err = wrong.open(b, sealed)
	require.ErrorIs(t, err, errCopilotRerunCipher)
	history, err := newCopilotHistoryCipher(key)
	require.NoError(t, err)
	var payload any
	require.Error(t, history.open("project", "pipeline", "run", "snapshot", "definition", sealed, &payload))
}

func TestCopilotRerunCipherBounds(t *testing.T) {
	for _, n := range []int{0, 16, 24, 31, 33} {
		_, err := newCopilotRerunCipher(make([]byte, n))
		require.ErrorIs(t, err, errCopilotRerunCipher)
	}
	c, err := newCopilotRerunCipher(bytes.Repeat([]byte{0x23}, 32))
	require.NoError(t, err)
	b := copilotRerunCipherBinding{Version: 1, Role: "dispatch", ProjectID: "project", PipelineID: "pipeline", RunID: "run", ObjectID: "seal", ContentBinding: "v1:" + strings.Repeat("c", 64)}
	max := repo.CopilotRerunMaxDefinitionBytes - c.aead.NonceSize() - c.aead.Overhead()
	sealed, err := c.seal(b, bytes.Repeat([]byte{1}, max))
	require.NoError(t, err)
	require.Len(t, sealed, repo.CopilotRerunMaxDefinitionBytes)
	opened, err := c.open(b, sealed)
	require.NoError(t, err)
	require.Len(t, opened, max)
	for _, plain := range [][]byte{nil, {}, make([]byte, max+1)} {
		_, err := c.seal(b, plain)
		require.ErrorIs(t, err, errCopilotRerunCipher)
	}
	for _, encoded := range [][]byte{nil, sealed[:20], append(bytes.Clone(sealed), 0)} {
		out, err := c.open(b, encoded)
		require.ErrorIs(t, err, errCopilotRerunCipher)
		require.Nil(t, out)
	}
	var absent *copilotRerunCipher
	_, err = absent.seal(b, []byte("data"))
	require.ErrorIs(t, err, errCopilotRerunCipher)
	_, err = absent.open(b, sealed)
	require.ErrorIs(t, err, errCopilotRerunCipher)
}
