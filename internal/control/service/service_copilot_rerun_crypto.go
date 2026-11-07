// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"

	"github.com/arcentrix/arcentra/internal/control/copilot"
	"github.com/arcentrix/arcentra/internal/control/repo"
)

var errCopilotRerunCipher = errors.New("whole-job re-execution encrypted content unavailable")

// This codec has no ephemeral-key or plaintext fallback. Callers supply the
// existing operator-managed master key. It is deliberately independent of the
// history key domain and never returns an underlying crypto or payload error.
type copilotRerunCipher struct{ aead cipher.AEAD }

type copilotRerunCipherBinding struct {
	Version                                      uint32
	Role, ProjectID, PipelineID, RunID, ObjectID string
	ContentBinding                               string
}

func (b copilotRerunCipherBinding) aad() ([]byte, error) {
	if b.Version != 1 || (b.Role != "definition" && b.Role != "dispatch") {
		return nil, errCopilotRerunCipher
	}
	for _, id := range []string{b.ProjectID, b.PipelineID, b.RunID, b.ObjectID} {
		if !copilot.ValidID(id) {
			return nil, errCopilotRerunCipher
		}
	}
	if !strings.HasPrefix(b.ContentBinding, "v1:") || len(b.ContentBinding) != 67 {
		return nil, errCopilotRerunCipher
	}
	hash, err := hex.DecodeString(b.ContentBinding[3:])
	if err != nil || hex.EncodeToString(hash) != b.ContentBinding[3:] {
		return nil, errCopilotRerunCipher
	}
	return historyFrame("arcentra-copilot-rerun-v1", b.Role, b.ProjectID, b.PipelineID, b.RunID, b.ObjectID, b.ContentBinding), nil
}

func newCopilotRerunCipher(master []byte) (*copilotRerunCipher, error) {
	if len(master) != 32 {
		return nil, errCopilotRerunCipher
	}
	m := hmac.New(sha256.New, master)
	_, _ = m.Write([]byte("arcentra/copilot/whole-job-rerun/aes-gcm/v1"))
	block, err := aes.NewCipher(m.Sum(nil))
	if err != nil {
		return nil, errCopilotRerunCipher
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errCopilotRerunCipher
	}
	return &copilotRerunCipher{aead: aead}, nil
}

func (c *copilotRerunCipher) seal(b copilotRerunCipherBinding, plain []byte) ([]byte, error) {
	if c == nil || c.aead == nil || len(plain) == 0 || len(plain) > repo.CopilotRerunMaxDefinitionBytes-c.aead.NonceSize()-c.aead.Overhead() {
		return nil, errCopilotRerunCipher
	}
	aad, err := b.aad()
	if err != nil {
		return nil, errCopilotRerunCipher
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, errCopilotRerunCipher
	}
	return c.aead.Seal(nonce, nonce, plain, aad), nil
}

func (c *copilotRerunCipher) open(b copilotRerunCipherBinding, encoded []byte) ([]byte, error) {
	if c == nil || c.aead == nil || len(encoded) > repo.CopilotRerunMaxDefinitionBytes {
		return nil, errCopilotRerunCipher
	}
	n := c.aead.NonceSize()
	if len(encoded) <= n+c.aead.Overhead() {
		return nil, errCopilotRerunCipher
	}
	aad, err := b.aad()
	if err != nil {
		return nil, errCopilotRerunCipher
	}
	plain, err := c.aead.Open(nil, encoded[:n], encoded[n:], aad)
	if err != nil {
		return nil, errCopilotRerunCipher
	}
	return plain, nil
}
