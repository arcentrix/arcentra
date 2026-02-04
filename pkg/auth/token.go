// Copyright 2025 Infraflows Team
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

package auth

import (
	"errors"
	"strings"

	"github.com/arcentrix/arcade/pkg/http"
	"github.com/arcentrix/arcade/pkg/http/jwt"
	"github.com/gofiber/fiber/v2"
)

// ParseAuthorizationToken 解析 Authorization 头中的 Bearer token
func ParseAuthorizationToken(f *fiber.Ctx, secretKey string) (*jwt.AuthClaims, error) {
	token := f.Get("Authorization")
	if token == "" {
		return nil, errors.New(http.TokenBeEmpty.Msg)
	}

	if t, ok := strings.CutPrefix(token, "Bearer "); ok {
		token = t
	} else {
		return nil, errors.New(http.TokenFormatIncorrect.Msg)
	}

	claims, err := jwt.ParseToken(token, secretKey)
	if err != nil {
		return nil, err
	}
	return claims, nil
}
