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

package service

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/arcentrix/arcentra/internal/engine/model"
	secretrepo "github.com/arcentrix/arcentra/internal/engine/repo"
	"github.com/arcentrix/arcentra/pkg/id"
	"github.com/arcentrix/arcentra/pkg/log"
	"gorm.io/gorm"
)

type SecretService struct {
	secretRepo secretrepo.ISecretRepository
	encryptKey []byte // 32 bytes for AES-256
}

func NewSecretService(secretRepo secretrepo.ISecretRepository) *SecretService {
	// TODO: load encryption key from config or environment variable
	// For now, using a default key (should be replaced in production)
	encryptKey := []byte("arcentra-secret-encryption-key-32b") // 32 bytes for AES-256

	return &SecretService{
		secretRepo: secretRepo,
		encryptKey: encryptKey,
	}
}

// encrypt encrypts plain text using AES-256-GCM
func (ss *SecretService) encrypt(plainText string) (string, error) {
	block, err := aes.NewCipher(ss.encryptKey)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}

	cipherText := gcm.Seal(nonce, nonce, []byte(plainText), nil)
	return base64.StdEncoding.EncodeToString(cipherText), nil
}

// decrypt decrypts cipher text using AES-256-GCM
func (ss *SecretService) decrypt(cipherText string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(cipherText)
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(ss.encryptKey)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", errors.New("invalid cipher text")
	}

	nonce, cipherTextBytes := data[:nonceSize], data[nonceSize:]
	plainText, err := gcm.Open(nil, nonce, cipherTextBytes, nil)
	if err != nil {
		return "", err
	}

	return string(plainText), nil
}

// CreateSecret creates a new secret.
func (ss *SecretService) CreateSecret(ctx context.Context, secretEntity *model.Secret, createdBy string) error {
	// validate required fields
	if secretEntity.Name == "" {
		return errors.New("name is required")
	}
	if secretEntity.SecretType == "" {
		return errors.New("secretType is required")
	}
	if secretEntity.SecretValue == "" {
		return errors.New("secretValue is required")
	}
	if secretEntity.Scope == "" {
		return errors.New("scope is required")
	}

	// validate secret type
	validTypes := []string{"password", "token", "ssh_key", "env"}
	isValidType := slices.Contains(validTypes, secretEntity.SecretType)
	if !isValidType {
		return fmt.Errorf("invalid secretType, must be one of: %v", validTypes)
	}

	// validate scope
	validScopes := []string{"global", "pipeline", "user", "project", "team"}
	isValidScope := slices.Contains(validScopes, secretEntity.Scope)
	if !isValidScope {
		return fmt.Errorf("invalid scope, must be one of: %v", validScopes)
	}

	// encrypt secret value
	encryptedValue, err := ss.encrypt(secretEntity.SecretValue)
	if err != nil {
		log.Errorw("failed to encrypt secret value", "error", err)
		return errors.New("failed to encrypt secret value")
	}

	// generate secret ID
	secretEntity.SecretId = id.GetUild()
	secretEntity.SecretValue = encryptedValue
	secretEntity.CreatedBy = createdBy

	if err := ss.secretRepo.Create(ctx, secretEntity); err != nil {
		log.Errorw("failed to create secret", "error", err)
		return errors.New("failed to create secret")
	}

	log.Infow("secret created successfully", "secretId", secretEntity.SecretId)
	return nil
}

// UpdateSecret updates a secret.
func (ss *SecretService) UpdateSecret(ctx context.Context, secretId string, secretEntity *model.Secret) error {
	_, err := ss.secretRepo.Get(ctx, secretId)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errors.New("secret not found")
		}
		log.Errorw("failed to get secret", "secretId", secretId, "error", err)
		return errors.New("failed to get secret")
	}

	// if updating secret value, encrypt it
	if secretEntity.SecretValue != "" {
		encryptedValue, err := ss.encrypt(secretEntity.SecretValue)
		if err != nil {
			log.Errorw("failed to encrypt secret value", "secretId", secretId, "error", err)
			return errors.New("failed to encrypt secret value")
		}
		secretEntity.SecretValue = encryptedValue
	}

	secretEntity.SecretId = secretId

	if err := ss.secretRepo.Update(ctx, secretEntity); err != nil {
		log.Errorw("failed to update secret", "secretId", secretId, "error", err)
		return errors.New("failed to update secret")
	}

	log.Infow("secret updated successfully", "secretId", secretId)
	return nil
}

// GetSecretByID gets a secret by ID (without decrypting value).
func (ss *SecretService) GetSecretByID(ctx context.Context, secretId string) (*model.Secret, error) {
	secretByID, err := ss.secretRepo.Get(ctx, secretId)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("secretByID not found")
		}
		log.Errorw("failed to get secretByID", "secretId", secretId, "error", err)
		return nil, errors.New("failed to get secretByID")
	}

	// mask secretByID value for security
	secretByID.SecretValue = "***MASKED***"
	return secretByID, nil
}

// GetSecretValue gets the decrypted secret value (use with caution).
func (ss *SecretService) GetSecretValue(ctx context.Context, secretId string) (string, error) {
	encryptedValue, err := ss.secretRepo.GetValue(ctx, secretId)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", errors.New("secret not found")
		}
		log.Errorw("failed to get secret value", "secretId", secretId, "error", err)
		return "", errors.New("failed to get secret value")
	}

	// decrypt secret value
	plainValue, err := ss.decrypt(encryptedValue)
	if err != nil {
		log.Errorw("failed to decrypt secret value", "secretId", secretId, "error", err)
		return "", errors.New("failed to decrypt secret value")
	}

	return plainValue, nil
}

// GetSecretList gets secret list with pagination and filters.
func (ss *SecretService) GetSecretList(ctx context.Context, pageNum, pageSize int, secretType, scope, scopeId, createdBy string) ([]*model.Secret, int64, error) {
	// set default pagination
	if pageNum <= 0 {
		pageNum = 1
	}
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 20
	}

	secrets, total, err := ss.secretRepo.List(ctx, pageNum, pageSize, secretType, scope, scopeId, createdBy)
	if err != nil {
		log.Errorw("failed to get secret list", "error", err)
		return nil, 0, errors.New("failed to get secret list")
	}

	// secret values are already excluded in repo layer
	return secrets, total, nil
}

// DeleteSecret deletes a secret.
func (ss *SecretService) DeleteSecret(ctx context.Context, secretId string) error {
	_, err := ss.secretRepo.Get(ctx, secretId)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errors.New("secret not found")
		}
		log.Errorw("failed to get secret", "secretId", secretId, "error", err)
		return errors.New("failed to get secret")
	}

	if err := ss.secretRepo.Delete(ctx, secretId); err != nil {
		log.Errorw("failed to delete secret", "secretId", secretId, "error", err)
		return errors.New("failed to delete secret")
	}

	log.Infow("secret deleted successfully", "secretId", secretId)
	return nil
}

// GetSecretsByScope gets secrets by scope and scope_id.
func (ss *SecretService) GetSecretsByScope(ctx context.Context, scope, scopeId string) ([]*model.Secret, error) {
	secrets, err := ss.secretRepo.ListByScope(ctx, scope, scopeId)
	if err != nil {
		log.Errorw("failed to get secrets by scope", "scope", scope, "scopeId", scopeId, "error", err)
		return nil, errors.New("failed to get secrets by scope")
	}
	return secrets, nil
}
