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

package rocketmq

import (
	"fmt"

	"github.com/apache/rocketmq-client-go/v2/primitive"
	"github.com/arcentrix/arcentra/pkg/mq"
)

// ClientConfig represents RocketMQ shared client configuration.
type ClientConfig struct {
	NameServers []string `json:"nameServers" mapstructure:"nameServers"`

	AccessKey   string                 `json:"accessKey" mapstructure:"accessKey"`
	SecretKey   string                 `json:"secretKey" mapstructure:"secretKey"`
	Credentials *primitive.Credentials `json:"credentials" mapstructure:"credentials"`
}

// ClientOption defines optional configuration for ClientConfig.
type ClientOption interface {
	apply(*ClientConfig)
}

type clientOptionFunc func(*ClientConfig)

func (fn clientOptionFunc) apply(cfg *ClientConfig) {
	fn(cfg)
}

func WithAccessKey(accessKey string) ClientOption {
	return clientOptionFunc(func(cfg *ClientConfig) {
		cfg.AccessKey = accessKey
	})
}

func WithSecretKey(secretKey string) ClientOption {
	return clientOptionFunc(func(cfg *ClientConfig) {
		cfg.SecretKey = secretKey
	})
}

func WithCredentials(credentials *primitive.Credentials) ClientOption {
	return clientOptionFunc(func(cfg *ClientConfig) {
		cfg.Credentials = credentials
	})
}

// RocketMQClient holds a base client configuration.
type RocketMQClient struct {
	Config ClientConfig
}

// NewRocketMQClient creates a new RocketMQClient using options.
func NewRocketMQClient(nameServers []string, opts ...ClientOption) (*RocketMQClient, error) {
	cfg := ClientConfig{
		NameServers: nameServers,
	}
	for _, opt := range opts {
		opt.apply(&cfg)
	}
	if err := mq.RequireNonEmptySlice("nameServers", cfg.NameServers); err != nil {
		return nil, err
	}
	return &RocketMQClient{Config: cfg}, nil
}

func resolveCredentials(cfg ClientConfig) (*primitive.Credentials, error) {
	if cfg.Credentials != nil {
		return cfg.Credentials, nil
	}
	if cfg.AccessKey == "" && cfg.SecretKey == "" {
		return nil, nil
	}
	if cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, fmt.Errorf("accessKey and secretKey are required together")
	}
	cred := primitive.Credentials{
		AccessKey: cfg.AccessKey,
		SecretKey: cfg.SecretKey,
	}
	return &cred, nil
}
