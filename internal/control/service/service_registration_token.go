// Copyright 2026 Arcentra Authors.
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
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/log"
	"golang.org/x/crypto/bcrypt"
)

const registrationTokenPrefix = "art_"

// RegistrationTokenService manages registration tokens for dynamic agent registration.
type RegistrationTokenService struct {
	tokenRepo repo.IRegistrationTokenRepository
}

// NewRegistrationTokenService creates a new RegistrationTokenService.
func NewRegistrationTokenService(tokenRepo repo.IRegistrationTokenRepository) *RegistrationTokenService {
	return &RegistrationTokenService{tokenRepo: tokenRepo}
}

// GenerateToken creates a new registration token and returns it in plain text.
// The plain token is only returned once; only bcrypt hash is stored in DB.
func (s *RegistrationTokenService) GenerateToken(
	ctx context.Context, req *model.CreateRegistrationTokenReq,
) (*model.CreateRegistrationTokenResp, error) {
	// Generate random bytes: art_ prefix + 20 bytes hex = 43 chars
	randomBytes := make([]byte, 20)
	if _, err := rand.Read(randomBytes); err != nil {
		return nil, fmt.Errorf("failed to generate random token: %w", err)
	}
	plainToken := registrationTokenPrefix + hex.EncodeToString(randomBytes)

	// Bcrypt hash the plain token
	hash, err := bcrypt.GenerateFromPassword([]byte(plainToken), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("failed to hash token: %w", err)
	}

	var expiresAt *time.Time
	if req.ExpiresAt != "" {
		t, err := time.Parse(time.RFC3339, req.ExpiresAt)
		if err != nil {
			return nil, fmt.Errorf("invalid expiresAt format: %w", err)
		}
		expiresAt = &t
	}

	record := &model.RegistrationToken{
		TokenHash:   string(hash),
		Description: req.Description,
		CreatedBy:   req.CreatedBy,
		ExpiresAt:   expiresAt,
		MaxUses:     req.MaxUses,
		UseCount:    0,
		IsActive:    1,
	}

	if err := s.tokenRepo.Create(ctx, record); err != nil {
		log.Errorw("failed to create registration token", "error", err)
		return nil, err
	}

	var expiresAtStr *string
	if expiresAt != nil {
		s := expiresAt.Format(time.RFC3339)
		expiresAtStr = &s
	}

	return &model.CreateRegistrationTokenResp{
		ID:          record.ID,
		Token:       plainToken,
		Description: record.Description,
		CreatedBy:   record.CreatedBy,
		ExpiresAt:   expiresAtStr,
		MaxUses:     record.MaxUses,
	}, nil
}

// ValidateToken validates a plain-text registration token against all active tokens.
// Returns the token record ID if valid, or an error.
func (s *RegistrationTokenService) ValidateToken(ctx context.Context, plainToken string) (uint64, error) {
	activeTokens, err := s.tokenRepo.GetAllActive(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to list active tokens: %w", err)
	}

	for _, t := range activeTokens {
		// Check expiration
		if t.ExpiresAt != nil && time.Now().After(*t.ExpiresAt) {
			continue
		}
		// Check max uses
		if t.MaxUses > 0 && t.UseCount >= t.MaxUses {
			continue
		}
		// Compare bcrypt hash
		if err := bcrypt.CompareHashAndPassword([]byte(t.TokenHash), []byte(plainToken)); err == nil {
			return t.ID, nil
		}
	}

	return 0, fmt.Errorf("invalid or expired registration token")
}

// ListTokens returns a paginated list of registration tokens (without plain tokens).
func (s *RegistrationTokenService) ListTokens(ctx context.Context, page, size int) ([]model.ListRegistrationTokenResp, int64, error) {
	tokens, count, err := s.tokenRepo.List(ctx, page, size)
	if err != nil {
		return nil, 0, err
	}

	result := make([]model.ListRegistrationTokenResp, len(tokens))
	for i, t := range tokens {
		var expiresAt *string
		if t.ExpiresAt != nil {
			s := t.ExpiresAt.Format(time.RFC3339)
			expiresAt = &s
		}
		result[i] = model.ListRegistrationTokenResp{
			ID:          t.ID,
			Description: t.Description,
			CreatedBy:   t.CreatedBy,
			ExpiresAt:   expiresAt,
			MaxUses:     t.MaxUses,
			UseCount:    t.UseCount,
			IsActive:    t.IsActive,
			CreatedAt:   t.CreatedAt.Format(time.RFC3339),
		}
	}
	return result, count, nil
}

// RevokeToken deactivates a registration token.
func (s *RegistrationTokenService) RevokeToken(ctx context.Context, id uint64) error {
	return s.tokenRepo.Deactivate(ctx, id)
}
