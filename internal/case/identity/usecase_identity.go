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

package identity

import (
	"context"
	"fmt"

	"github.com/arcentrix/arcentra/internal/domain/identity"
)

type UserInfo struct {
	UserID   string `json:"userId"`
	Username string `json:"username"`
	Email    string `json:"email"`
}

// ListAvailableProviders returns enabled identity providers stripped of all
// sensitive configuration. Designed for unauthenticated access on the login
// page so the frontend can render third-party login buttons.
func (uc *ManageUserUseCase) ListAvailableProviders(ctx context.Context) ([]AvailableProvider, error) {
	if uc.idpRepo == nil {
		return nil, fmt.Errorf("identity provider repository not configured")
	}

	providers, err := uc.idpRepo.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list identity providers: %w", err)
	}

	var result []AvailableProvider
	for i := range providers {
		p := &providers[i]
		if !p.IsEnabled {
			continue
		}

		ap := AvailableProvider{
			Name:         p.Name,
			ProviderType: string(p.ProviderType),
			Description:  p.Description,
			Priority:     p.Priority,
		}

		switch p.ProviderType {
		case identity.ProviderTypeOAuth, identity.ProviderTypeOIDC:
			ap.LoginMode = "redirect"
			ap.AuthURL = fmt.Sprintf("/api/v1/identity/authorize/%s", p.Name)
		case identity.ProviderTypeLDAP:
			ap.LoginMode = "form"
			ap.AuthURL = fmt.Sprintf("/api/v1/identity/ldap/login/%s", p.Name)
		case identity.ProviderTypeSAML:
			ap.LoginMode = "redirect"
			ap.AuthURL = fmt.Sprintf("/api/v1/identity/authorize/%s", p.Name)
		default:
			ap.LoginMode = "redirect"
			ap.AuthURL = fmt.Sprintf("/api/v1/identity/authorize/%s", p.Name)
		}

		result = append(result, ap)
	}
	return result, nil
}

func (uc *ManageUserUseCase) Authorize(ctx context.Context, providerName string) (string, error) {
	if uc.idpRepo == nil {
		return "", fmt.Errorf("identity provider repository not configured")
	}
	provider, err := uc.idpRepo.Get(ctx, providerName)
	if err != nil {
		return "", fmt.Errorf("provider not found: %w", err)
	}
	if !provider.IsEnabled {
		return "", fmt.Errorf("provider is disabled")
	}

	return fmt.Sprintf("/identity/callback/%s", providerName), nil
}

func (uc *ManageUserUseCase) Callback(ctx context.Context, providerName, state, code string) (*UserInfo, error) {
	return nil, fmt.Errorf("OAuth callback not yet implemented in use case layer")
}

func (uc *ManageUserUseCase) LDAPLogin(ctx context.Context, providerName, username, password string) (*UserInfo, error) {
	return nil, fmt.Errorf("LDAP login not yet implemented in use case layer")
}

func (uc *ManageUserUseCase) ListProviders(ctx context.Context, providerType string) ([]identity.IdentityProvider, error) {
	if uc.idpRepo == nil {
		return nil, fmt.Errorf("identity provider repository not configured")
	}
	if providerType != "" {
		return uc.idpRepo.GetByType(ctx, identity.ProviderType(providerType))
	}
	return uc.idpRepo.List(ctx)
}

func (uc *ManageUserUseCase) GetProvider(ctx context.Context, name string) (*identity.IdentityProvider, error) {
	if uc.idpRepo == nil {
		return nil, fmt.Errorf("identity provider repository not configured")
	}
	return uc.idpRepo.Get(ctx, name)
}

func (uc *ManageUserUseCase) GetProviderTypes(ctx context.Context) ([]string, error) {
	if uc.idpRepo == nil {
		return nil, fmt.Errorf("identity provider repository not configured")
	}
	return uc.idpRepo.ListTypes(ctx)
}

func (uc *ManageUserUseCase) CreateProvider(ctx context.Context, data map[string]any) error {
	if uc.idpRepo == nil {
		return fmt.Errorf("identity provider repository not configured")
	}
	provider := &identity.IdentityProvider{
		Name:         data["name"].(string),
		ProviderType: identity.ProviderType(data["providerType"].(string)),
	}
	if desc, ok := data["description"].(string); ok {
		provider.Description = desc
	}
	provider.IsEnabled = true
	return uc.idpRepo.Create(ctx, provider)
}

func (uc *ManageUserUseCase) UpdateProvider(ctx context.Context, name string, data map[string]any) error {
	if uc.idpRepo == nil {
		return fmt.Errorf("identity provider repository not configured")
	}
	provider, err := uc.idpRepo.Get(ctx, name)
	if err != nil {
		return err
	}
	if desc, ok := data["description"].(string); ok {
		provider.Description = desc
	}
	return uc.idpRepo.Update(ctx, name, provider)
}

func (uc *ManageUserUseCase) ToggleProvider(ctx context.Context, name string) error {
	if uc.idpRepo == nil {
		return fmt.Errorf("identity provider repository not configured")
	}
	return uc.idpRepo.Toggle(ctx, name)
}

func (uc *ManageUserUseCase) DeleteProvider(ctx context.Context, name string) error {
	if uc.idpRepo == nil {
		return fmt.Errorf("identity provider repository not configured")
	}
	return uc.idpRepo.Delete(ctx, name)
}
