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

package identity

type LoginInput struct {
	Username string
	Email    string
	Password string
}

type RegisterInput struct {
	UserID   string
	Username string
	FullName string
	Email    string
	Password string
}

type UpdateUserInput struct {
	FullName  *string
	Avatar    *string
	Email     *string
	Phone     *string
	IsEnabled *bool
}

type CreateRoleInput struct {
	RoleID      string
	Name        string
	DisplayName string
	Description string
}

type CreateTeamInput struct {
	OrgID       string
	Name        string
	DisplayName string
	Description string
	Visibility  int
}

// AvailableProvider is a safe, frontend-facing representation of an enabled
// identity provider. It intentionally omits all credential/secret fields so
// that it can be served without authentication on the login page.
type AvailableProvider struct {
	Name         string `json:"name"`
	ProviderType string `json:"providerType"`
	Description  string `json:"description"`
	Priority     int    `json:"priority"`
	AuthURL      string `json:"authUrl"`
	LoginMode    string `json:"loginMode"`
}
