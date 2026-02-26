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

// ClientOption defines optional configuration for ClientConfig.
type ClientOption interface {
	apply(*Config)
}

type clientOptionFunc func(*Config)

func (fn clientOptionFunc) apply(cfg *Config) {
	fn(cfg)
}

func WithSecurityProtocol(securityProtocol string) ClientOption {
	return clientOptionFunc(func(cfg *Config) {
		cfg.SecurityProtocol = securityProtocol
	})
}

func WithSaslMechanism(mechanism string) ClientOption {
	return clientOptionFunc(func(cfg *Config) {
		cfg.Sasl.Mechanism = mechanism
	})
}

func WithSaslUsername(username string) ClientOption {
	return clientOptionFunc(func(cfg *Config) {
		cfg.Sasl.Username = username
	})
}

func WithSaslPassword(password string) ClientOption {
	return clientOptionFunc(func(cfg *Config) {
		cfg.Sasl.Password = password
	})
}

func WithSslCaFile(path string) ClientOption {
	return clientOptionFunc(func(cfg *Config) {
		cfg.Ssl.CaFile = path
	})
}

func WithSslCertFile(path string) ClientOption {
	return clientOptionFunc(func(cfg *Config) {
		cfg.Ssl.CertFile = path
	})
}

func WithSslKeyFile(path string) ClientOption {
	return clientOptionFunc(func(cfg *Config) {
		cfg.Ssl.KeyFile = path
	})
}

func WithSslPassword(password string) ClientOption {
	return clientOptionFunc(func(cfg *Config) {
		cfg.Ssl.Password = password
	})
}

// Client holds a base client configuration.
type Client struct {
	Config Config
}

// NewKafkaClient creates a new Client using options.
func NewKafkaClient(bootstrapServers string, opts ...ClientOption) (*Client, error) {
	cfg := Config{
		BootstrapServers: bootstrapServers,
	}
	for _, opt := range opts {
		opt.apply(&cfg)
	}
	if err := mq.RequireNonEmpty("bootstrapServers", cfg.BootstrapServers); err != nil {
		return nil, err
	}
	return &Client{Config: cfg}, nil
}

func buildBaseConfig(cfg Config) (*kafka.ConfigMap, error) {
	if err := mq.RequireNonEmpty("bootstrapServers", cfg.BootstrapServers); err != nil {
		return nil, err
	}

	config := &kafka.ConfigMap{
		"bootstrap.servers":        cfg.BootstrapServers,
		"security.protocol":        cfg.SecurityProtocol,
		"sasl.mechanism":           cfg.Sasl.Mechanism,
		"sasl.username":            cfg.Sasl.Username,
		"sasl.password":            cfg.Sasl.Password,
		"ssl.ca.location":          cfg.Ssl.CaFile,
		"ssl.certificate.location": cfg.Ssl.CertFile,
		"ssl.key.location":         cfg.Ssl.KeyFile,
		"ssl.key.password":         cfg.Ssl.Password,
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
