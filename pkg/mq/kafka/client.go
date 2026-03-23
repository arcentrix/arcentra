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

package kafka

import (
	"fmt"
	"os"
	"strings"

	"github.com/arcentrix/arcentra/pkg/mq"
	"github.com/confluentinc/confluent-kafka-go/v2/kafka"
)

// Config represents Kafka shared client configuration.
type Config struct {
	BootstrapServers string     `mapstructure:"bootstrapServers"`
	Acks             string     `mapstructure:"acks"`
	Retries          int        `mapstructure:"retries"`
	Compression      string     `mapstructure:"compression"`
	SecurityProtocol string     `mapstructure:"securityProtocol"`
	Sasl             SaslConfig `mapstructure:"sasl"`
	Ssl              SslConfig  `mapstructure:"ssl"`
}

type SaslConfig struct {
	Mechanism string `mapstructure:"mechanism"`
	Username  string `mapstructure:"username"`
	Password  string `mapstructure:"password"`
}

type SslConfig struct {
	CaFile   string `mapstructure:"caFile"`
	CertFile string `mapstructure:"certFile"`
	KeyFile  string `mapstructure:"keyFile"`
	Password string `mapstructure:"password"`
}

// Option defines optional configuration for ClientConfig.
type Option interface {
	apply(*Config)
}

type optionFunc func(*Config)

func (fn optionFunc) apply(conf *Config) {
	fn(conf)
}

func WithSecurityProtocol(securityProtocol string) Option {
	return optionFunc(func(conf *Config) {
		conf.SecurityProtocol = securityProtocol
	})
}

func WithSaslMechanism(mechanism string) Option {
	return optionFunc(func(conf *Config) {
		conf.Sasl.Mechanism = mechanism
	})
}

func WithSaslUsername(username string) Option {
	return optionFunc(func(conf *Config) {
		conf.Sasl.Username = username
	})
}

func WithSaslPassword(password string) Option {
	return optionFunc(func(conf *Config) {
		conf.Sasl.Password = password
	})
}

func WithSslCaFile(path string) Option {
	return optionFunc(func(conf *Config) {
		conf.Ssl.CaFile = path
	})
}

func WithSslCertFile(path string) Option {
	return optionFunc(func(conf *Config) {
		conf.Ssl.CertFile = path
	})
}

func WithSslKeyFile(path string) Option {
	return optionFunc(func(conf *Config) {
		conf.Ssl.KeyFile = path
	})
}

func WithSslPassword(password string) Option {
	return optionFunc(func(conf *Config) {
		conf.Ssl.Password = password
	})
}

// Client holds a base client configuration.
type Client struct {
	Config Config
}

// NewKafkaClient creates a new Client using options.
func NewKafkaClient(bootstrapServers string, opts ...Option) (*Client, error) {
	conf := Config{
		BootstrapServers: bootstrapServers,
	}
	for _, opt := range opts {
		opt.apply(&conf)
	}
	if err := mq.RequireNonEmpty("bootstrapServers", conf.BootstrapServers); err != nil {
		return nil, err
	}
	return &Client{Config: conf}, nil
}

func buildBaseConfig(conf Config) (*kafka.ConfigMap, error) {
	if err := mq.RequireNonEmpty("bootstrapServers", conf.BootstrapServers); err != nil {
		return nil, err
	}

	config := &kafka.ConfigMap{
		"bootstrap.servers":        conf.BootstrapServers,
		"security.protocol":        conf.SecurityProtocol,
		"sasl.mechanism":           conf.Sasl.Mechanism,
		"sasl.username":            conf.Sasl.Username,
		"sasl.password":            conf.Sasl.Password,
		"ssl.ca.location":          conf.Ssl.CaFile,
		"ssl.certificate.location": conf.Ssl.CertFile,
		"ssl.key.location":         conf.Ssl.KeyFile,
		"ssl.key.password":         conf.Ssl.Password,
	}

	return config, nil
}

func buildClientId(clientId string) (string, error) {
	if err := mq.RequireNonEmpty("clientId", clientId); err != nil {
		return "", err
	}
	hostname, err := os.Hostname()
	if err != nil || strings.TrimSpace(hostname) == "" {
		hostname = "UNKNOWN"
	}
	return strings.ToUpper(fmt.Sprintf("%s_CLIENT_%s", clientId, hostname)), nil
}
