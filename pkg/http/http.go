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

package http

import (
	"time"
)

type Http struct {
	Host            string
	Port            int
	AccessLog       bool
	ReadTimeout     int
	WriteTimeout    int
	IdleTimeout     int
	ShutdownTimeout int
	BodyLimit       int // 请求体大小限制（字节），默认 100MB
	Auth            Auth
}

type Auth struct {
	SecretKey     string
	AccessExpire  time.Duration
	RefreshExpire time.Duration
}

// TokenInfo token information stored in Redis
type TokenInfo struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpireAt     int64  `json:"expireAt"`
	CreateAt     int64  `json:"createAt"`
}

func (h *Http) SetDefaults() {
	if h.Host == "" {
		h.Host = "127.0.0.1"
	}
	if h.Port == 0 {
		h.Port = 8080
	}
	if h.ReadTimeout == 0 {
		h.ReadTimeout = 60
	}
	if h.WriteTimeout == 0 {
		h.WriteTimeout = 60
	}
	if h.IdleTimeout == 0 {
		h.IdleTimeout = 60
	}
	if h.ShutdownTimeout == 0 {
		h.ShutdownTimeout = 10
	}
	if h.BodyLimit == 0 {
		h.BodyLimit = 100 * 1024 * 1024 // 100MB
	}
	if h.Auth.AccessExpire == 0 {
		h.Auth.AccessExpire = 3600 * time.Minute
	}
	if h.Auth.RefreshExpire == 0 {
		h.Auth.RefreshExpire = 7200 * time.Minute
	}
}
