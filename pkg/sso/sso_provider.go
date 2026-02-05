// Copyright 2025 Arcentra Team
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

package sso

import (
	"context"

	"golang.org/x/oauth2"
)

type ISSOProvider interface {
	AuthCodeURL(state string) string
	ExchangeToken(ctx context.Context, code string) (*oauth2.Token, error)
	GetUserInfo(ctx context.Context, token *oauth2.Token) (*UserInfoAdapter, error)
}

type UserInfoAdapter struct {
	ID        string `json:"sub"`
	Email     string `json:"email"`
	Name      string `json:"name"`
	Username  string `json:"preferred_username"`
	Picture   string `json:"picture"`
	Nickname  string `json:"nickname"`
	AvatarURL string `json:"avatar_url"`
}
