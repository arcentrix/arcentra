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

package http

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	duration "github.com/arcentrix/arcentra/pkg/time"
	"github.com/gofiber/fiber/v2"
	"github.com/spf13/viper"
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
	AccessToken  string `json:"accessToken,omitempty"`
	RefreshToken string `json:"refreshToken,omitempty"`
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

	// Normalize auth expire units.
	//
	// Config files document these values as "minutes" and often provide plain numbers
	// (e.g. accessExpire = 3600). When unmarshaled into time.Duration via mapstructure,
	// a plain number becomes nanoseconds. That makes tokens expire almost immediately.
	//
	// Rule:
	// - If value is < 1 minute, treat it as "minutes" and convert.
	// - If value is already a duration string (e.g. "60m", "1h"), keep as-is.
	if h.Auth.AccessExpire > 0 && h.Auth.AccessExpire < time.Minute {
		h.Auth.AccessExpire = h.Auth.AccessExpire * time.Minute
	}
	if h.Auth.RefreshExpire > 0 && h.Auth.RefreshExpire < time.Minute {
		h.Auth.RefreshExpire = h.Auth.RefreshExpire * time.Minute
	}
}

// QueryInt queries the int value from the query string
func (h *Http) QueryInt(c *fiber.Ctx, key string) int {
	value := c.Query(key)
	if value == "" {
		return 0
	}
	intValue, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return intValue
}

// parseAuthExpire parses the auth expire from the config
func parseAuthExpire(config *viper.Viper, key string) (string, bool) {
	if config == nil || key == "" {
		return "", false
	}
	if !config.IsSet(key) {
		return "", false
	}
	s := strings.TrimSpace(config.GetString(key))
	if s == "" {
		return "", false
	}
	// Backward-compat: allow plain number minutes, e.g. "3600" => "3600m".
	allDigits := true
	for _, r := range s {
		if r < '0' || r > '9' {
			allDigits = false
			break
		}
	}
	if allDigits {
		s += "m"
	}
	return s, true
}

// ApplyHTTPAuthExpiry applies the auth expiry to the http config
func ApplyHTTPAuthExpiry(config *viper.Viper, httpCfg *Http) error {
	if httpCfg == nil {
		return nil
	}

	if s, ok := parseAuthExpire(config, "http.auth.accessExpire"); ok {
		d, err := duration.Parse(s)
		if err != nil {
			return fmt.Errorf("parse http.auth.accessExpire=%q: %w", s, err)
		}
		httpCfg.Auth.AccessExpire = d
	}
	if s, ok := parseAuthExpire(config, "http.auth.refreshExpire"); ok {
		d, err := duration.Parse(s)
		if err != nil {
			return fmt.Errorf("parse http.auth.refreshExpire=%q: %w", s, err)
		}
		httpCfg.Auth.RefreshExpire = d
	}
	return nil
}
