// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
)

// No change is made to SecretService's existing ciphertext format. History has
// a domain-separated key and exact, length-framed AEAD identity binding.
type copilotHistoryCipher struct {
	aead       cipher.AEAD
	bindingKey []byte
}

func newCopilotHistoryCipher(master []byte) (*copilotHistoryCipher, error) {
	if len(master) != 32 {
		return nil, ErrCopilotHistoryUnavailable
	}
	derive := func(domain string) []byte {
		m := hmac.New(sha256.New, master)
		m.Write([]byte(domain))
		return m.Sum(nil)
	}
	block, err := aes.NewCipher(derive("arcentra/copilot/history/aes-gcm/v1"))
	if err != nil {
		return nil, ErrCopilotHistoryUnavailable
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrCopilotHistoryUnavailable
	}
	return &copilotHistoryCipher{aead: aead, bindingKey: derive("arcentra/copilot/history/binding/v1")}, nil
}
func historyFrame(values ...string) []byte {
	var out []byte
	for _, v := range values {
		out = binary.BigEndian.AppendUint64(out, uint64(len(v)))
		out = append(out, v...)
	}
	return out
}
func (c *copilotHistoryCipher) seal(owner, project, conversation, object, role string, payload any) ([]byte, error) {
	if c == nil {
		return nil, ErrCopilotHistoryUnavailable
	}
	plain, err := json.Marshal(payload)
	if err != nil || len(plain) > 96*1024 {
		return nil, ErrCopilotHistoryContent
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, ErrCopilotHistoryUnavailable
	}
	aad := historyFrame("arcentra-copilot-history-v1", owner, project, conversation, object, role)
	return c.aead.Seal(nonce, nonce, plain, aad), nil
}
func (c *copilotHistoryCipher) open(owner, project, conversation, object, role string, encoded []byte, payload any) error {
	if c == nil {
		return ErrCopilotHistoryUnavailable
	}
	n := c.aead.NonceSize()
	if len(encoded) < n+c.aead.Overhead() || len(encoded) > 128*1024 {
		return ErrCopilotHistoryContent
	}
	plain, err := c.aead.Open(nil, encoded[:n], encoded[n:], historyFrame("arcentra-copilot-history-v1", owner, project, conversation, object, role))
	if err != nil || len(plain) > 96*1024 {
		return ErrCopilotHistoryContent
	}
	if json.Unmarshal(plain, payload) != nil {
		return ErrCopilotHistoryContent
	}
	return nil
}
func historyMAC(key []byte, values ...string) string {
	m := hmac.New(sha256.New, key)
	m.Write(historyFrame(values...))
	return hex.EncodeToString(m.Sum(nil))
}
func historyHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
